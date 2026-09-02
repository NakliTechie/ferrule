package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// MCP is Ferrule's agent face: the control plane, not an inference path (§4.3.3).
//
// One manifest, two doors — the manifest is generated from the same command bus the
// panel dispatches through, so `manifest ⊇ command bus` holds by construction and the
// parity lint has something real to assert.
type MCP struct{ bus *Bus }

// NewMCP builds the agent face over a bus.
func NewMCP(b *Bus) *MCP { return &MCP{bus: b} }

// Tool is one entry of the manifest.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

// Manifest renders every control op as a tool. Person-only ops are listed and marked,
// never omitted: an agent should learn that an act exists and is not its to take.
func (m *MCP) Manifest() map[string]any {
	ops := m.bus.Ops()
	tools := make([]Tool, 0, len(ops))
	for _, o := range ops {
		props := map[string]any{}
		var required []string
		for _, p := range o.Params {
			schema := map[string]any{"description": p.Desc}
			switch p.Type {
			case "number":
				schema["type"] = "number"
			case "boolean":
				schema["type"] = "boolean"
			case "string[]":
				schema["type"] = "array"
				schema["items"] = map[string]any{"type": "string"}
			default:
				schema["type"] = "string"
			}
			if p.Secret {
				schema["writeOnly"] = true
			}
			props[p.Name] = schema
			if p.Required {
				required = append(required, p.Name)
			}
		}
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		tools = append(tools, Tool{
			Name: o.Name, Description: o.Desc, InputSchema: schema,
			Annotations: map[string]any{
				"readOnlyHint":    !o.Mutating,
				"destructiveHint": o.Mutating,
				"personOnly":      o.PersonOnly,
				"staged":          o.Mutating && !o.PersonOnly,
			},
		})
	}
	return map[string]any{
		"name": "ferrule", "version": "1", "tools": tools,
		"instructions": "Ferrule's control plane. Read ops answer immediately. Mutating " +
			"ops are staged and land only when the person applies them. Person-only ops " +
			"cannot be delegated. Inference does not go through here: point your client " +
			"at the OpenAI-compatible endpoint with a Ferrule app token.",
	}
}

// ServeHTTP speaks the MCP JSON-RPC shape for tools/list and tools/call.
func (m *MCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, m.Manifest())
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcErr(nil, -32700, err.Error()))
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name      string `json:"name"`
			Arguments Args   `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcErr(nil, -32700, err.Error()))
		return
	}
	caller := r.Header.Get("X-Ferrule-Caller")
	if caller == "" {
		caller = "mcp:" + r.RemoteAddr
	}

	switch req.Method {
	case "initialize":
		writeJSON(w, http.StatusOK, rpcOK(req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ferrule", "version": "1"},
		}))
	case "tools/list":
		writeJSON(w, http.StatusOK, rpcOK(req.ID, map[string]any{"tools": m.Manifest()["tools"]}))
	case "tools/call":
		m.call(w, r, req.ID, req.Params.Name, req.Params.Arguments, caller)
	default:
		writeJSON(w, http.StatusOK, rpcErr(req.ID, -32601, "unknown method "+req.Method))
	}
}

func (m *MCP) call(w http.ResponseWriter, r *http.Request, id json.RawMessage, name string, args Args, caller string) {
	op, ok := m.bus.Op(name)
	if !ok {
		writeJSON(w, http.StatusOK, rpcErr(id, -32602, "no control op "+name))
		return
	}
	if args == nil {
		args = Args{}
	}
	var (
		res any
		err error
	)
	switch {
	case op.PersonOnly:
		_ = m.bus.app.DB.RecordControl(op.Name, DoorMCP, caller, "refused:person-only")
		res, err = nil, errPersonOnly(op.Name)
	case op.Mutating:
		// Never auto-commit a mutating op (§4.8). It stages; the person applies.
		res, err = m.bus.Stage(op.Name, args, DoorMCP, caller)
	default:
		res, err = m.bus.Dispatch(r.Context(), op.Name, args, DoorMCP, caller)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, rpcOK(id, map[string]any{
			"isError": true,
			"content": []any{map[string]any{"type": "text", "text": err.Error()}},
		}))
		return
	}
	body, _ := json.MarshalIndent(res, "", "  ")
	writeJSON(w, http.StatusOK, rpcOK(id, map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(body)}},
		"structuredContent": res,
	}))
}

type personOnlyError string

func (e personOnlyError) Error() string { return string(e) }

func errPersonOnly(op string) error { return personOnlyError(personOnlyMsg(op)) }

func rpcOK(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id),
		"error": map[string]any{"code": code, "message": msg}}
}

func rawOrNull(id json.RawMessage) any {
	if len(strings.TrimSpace(string(id))) == 0 {
		return nil
	}
	return id
}
