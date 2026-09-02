// Package api is Ferrule's one command bus. The panel, the CLI's control verbs, and the
// MCP agent face all dispatch through the ops registered here — there is no second path,
// and the MCP manifest is generated from this same registry so parity is structural
// rather than remembered (§4.7, §4.8).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ferrule/internal/app"
)

// Door names where a control call came from.
const (
	DoorUI   = "ui"
	DoorCLI  = "cli"
	DoorMCP  = "mcp"
	DoorHTTP = "http"
)

// Args is one op's input.
type Args map[string]any

// Str reads a string argument.
func (a Args) Str(k string) string {
	if v, ok := a[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// Bool reads a boolean argument.
func (a Args) Bool(k string) bool { v, _ := a[k].(bool); return v }

// Int reads a numeric argument, tolerating JSON's float64.
func (a Args) Int(k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// Strs reads a list-of-strings argument.
func (a Args) Strs(k string) []string {
	raw, ok := a[k].([]any)
	if !ok {
		if ss, ok := a[k].([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Param describes one op input, for the manifest and for the panel's forms.
type Param struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Desc     string `json:"description"`
	// Secret marks a value that must never be staged, logged, or shown back. A secret
	// param is supplied by the person at apply time, never carried in a staged payload.
	Secret bool `json:"secret,omitempty"`
}

// Op is one control-plane operation.
type Op struct {
	Name string `json:"name"`
	Desc string `json:"description"`
	// Mutating ops stage before they land when they arrive through the agent door.
	Mutating bool `json:"mutating"`
	// PersonOnly ops are never delegable, and say so rather than being omitted (§4.7).
	PersonOnly bool    `json:"person_only"`
	Params     []Param `json:"params"`

	run func(context.Context, *app.App, Args) (any, error)
}

// Bus is the registry.
type Bus struct {
	app *app.App
	ops map[string]*Op
}

// NewBus registers every control op against a core.
func NewBus(a *app.App) *Bus {
	b := &Bus{app: a, ops: map[string]*Op{}}
	b.register()
	return b
}

// Ops returns every op, sorted by name.
func (b *Bus) Ops() []*Op {
	out := make([]*Op, 0, len(b.ops))
	for _, o := range b.ops {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Op looks up one op.
func (b *Bus) Op(name string) (*Op, bool) { o, ok := b.ops[name]; return o, ok }

func (b *Bus) add(o *Op) { b.ops[o.Name] = o }

// Dispatch runs an op directly. Callers that must stage (the agent door) check
// Op.Mutating first and call Stage instead.
func (b *Bus) Dispatch(ctx context.Context, name string, args Args, door, caller string) (any, error) {
	o, ok := b.ops[name]
	if !ok {
		return nil, fmt.Errorf("no control op %q", name)
	}
	for _, p := range o.Params {
		if p.Required && args.Str(p.Name) == "" && args[p.Name] == nil {
			return nil, fmt.Errorf("%s: %s is required", o.Name, p.Name)
		}
	}
	res, err := o.run(ctx, b.app, args)
	outcome := "ok"
	if err != nil {
		outcome = "error: " + err.Error()
	}
	_ = b.app.DB.RecordControl(o.Name, door, caller, outcome)
	return res, err
}

// Stage records a mutating op for a person to apply, stripping any secret parameter so a
// key never sits in the staging table (the vault invariant outranks convenience).
func (b *Bus) Stage(name string, args Args, door, caller string) (any, error) {
	o, ok := b.ops[name]
	if !ok {
		return nil, fmt.Errorf("no control op %q", name)
	}
	if o.PersonOnly {
		return nil, fmt.Errorf("%s", personOnlyMsg(o.Name))
	}
	clean := Args{}
	var withheld []string
	secret := map[string]bool{}
	for _, p := range o.Params {
		if p.Secret {
			secret[p.Name] = true
		}
	}
	for k, v := range args {
		if secret[k] {
			withheld = append(withheld, k)
			continue
		}
		clean[k] = v
	}
	payload, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	s, err := b.app.DB.Stage(o.Name, string(payload), door, caller)
	if err != nil {
		return nil, err
	}
	_ = b.app.DB.RecordControl(o.Name, door, caller, "staged:"+s.ID)
	return map[string]any{
		"staged": true, "id": s.ID, "op": o.Name, "args": clean,
		"withheld": withheld, "message": stagedMsg(o.Name),
	}, nil
}

// Apply lands a staged op. Extra supplies any secret parameter withheld at staging time;
// it comes from the person, at the moment they apply.
func (b *Bus) Apply(ctx context.Context, id string, extra Args, door, caller string) (any, error) {
	s, err := b.app.DB.StagedOp(id)
	if err != nil {
		return nil, fmt.Errorf("%s", stagedMissingMsg(id))
	}
	if s.AppliedAt != 0 {
		return nil, fmt.Errorf("staged op %s was already applied", id)
	}
	args := Args{}
	if err := json.Unmarshal([]byte(s.Payload), &args); err != nil {
		return nil, err
	}
	for k, v := range extra {
		args[k] = v
	}
	res, err := b.Dispatch(ctx, s.Op, args, door, caller)
	if err != nil {
		return nil, err
	}
	if err := b.app.DB.MarkApplied(id); err != nil {
		return nil, err
	}
	return map[string]any{"applied": id, "op": s.Op, "result": res,
		"message": stagedAppliedMsg(s.Op)}, nil
}
