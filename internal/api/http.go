package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ferrule/internal/app"
)

// API serves the control plane over HTTP for the embedded panel.
type API struct {
	app   *app.App
	bus   *Bus
	mcp   *MCP
	token *Token
}

// New builds the control-plane HTTP surface.
func New(a *app.App) *API {
	bus := NewBus(a)
	tok, err := NewToken()
	if err != nil {
		// Without a control token the control plane cannot be defended, so refusing to
		// start is the only honest outcome.
		panic("ferrule: cannot mint a control token: " + err.Error())
	}
	return &API{app: a, bus: bus, mcp: NewMCP(bus), token: tok}
}

// Token is the run's control-plane secret, for the panel the daemon serves itself.
func (s *API) Token() *Token { return s.token }

// SetLANEndpoint records the address other machines reach Ferrule at, so the panel can
// hand a newly minted token the URL that works from the machine it is for. Empty when
// Ferrule is not on the network.
func (s *API) SetLANEndpoint(ep string) { s.bus.lanEndpoint = ep }

// SetLANEndpoints records every address this machine serves, so the panel can offer the
// choice instead of presenting one guess as a fact.
func (s *API) SetLANEndpoints(eps []string) { s.bus.lanEndpoints = eps }

// Bus exposes the command bus (the CLI and the tests dispatch through it directly).
func (s *API) Bus() *Bus { return s.bus }

// Mount registers the control plane.
func (s *API) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/api/op/", s.handleOp)
	mux.HandleFunc("/api/manifest", s.handleManifest)
	mux.HandleFunc("/api/staged/", s.handleStaged)
	mux.HandleFunc("/mcp", s.mcp.ServeHTTP)
}

func (s *API) handleOp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/op/")
	if name == "" {
		http.Error(w, "name a control op", http.StatusNotFound)
		return
	}
	op, known := s.bus.Op(name)
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no control op " + name})
		return
	}
	// A mutation is never reachable by URL alone. This removes every attack that works
	// by getting a browser to fetch a address — images, scripts, navigations.
	if op.Mutating && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": name + " changes state and is POST-only"})
		return
	}
	if !s.token.Valid(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "the control plane needs this run's control token in " + HeaderName +
				"; only the panel this daemon served can have it"})
		return
	}
	args := Args{}
	if r.Method == http.MethodPost {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				http.Error(w, "arguments are not JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
	} else {
		for k, v := range r.URL.Query() {
			if len(v) == 1 {
				args[k] = v[0]
			} else {
				args[k] = toAny(v)
			}
		}
	}
	// The panel is the person's own door: mutations land directly. Staging exists for
	// the agent door, where nobody is watching.
	res, err := s.bus.Dispatch(r.Context(), op.Name, args, DoorUI, callerOf(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *API) handleManifest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mcp.Manifest())
}

func (s *API) handleStaged(w http.ResponseWriter, r *http.Request) {
	if !s.token.Valid(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "the control plane needs this run's control token in " + HeaderName})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/staged/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		res, err := s.bus.Dispatch(r.Context(), "list_staged", Args{}, DoorUI, callerOf(r))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if action != "" && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": "applying or discarding a staged operation is POST-only"})
		return
	}
	switch action {
	case "apply":
		extra := Args{}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			// Not swallowed: this body carries the secret a staged operation was denied,
			// so garbling it must not quietly apply the operation without one.
			if err := json.Unmarshal(raw, &extra); err != nil {
				writeJSON(w, http.StatusBadRequest,
					map[string]any{"error": "arguments are not JSON: " + err.Error()})
				return
			}
		}
		res, err := s.bus.Apply(r.Context(), id, extra, DoorUI, callerOf(r))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "discard":
		if err := s.app.DB.DiscardStaged(id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"discarded": id})
	default:
		op, err := s.app.DB.StagedOp(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, op)
	}
}

func callerOf(r *http.Request) string {
	if c := r.Header.Get("X-Ferrule-Caller"); c != "" {
		return c
	}
	if o := r.Header.Get("Origin"); o != "" {
		return o
	}
	return r.RemoteAddr
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
