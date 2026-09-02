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
	app *app.App
	bus *Bus
	mcp *MCP
}

// New builds the control-plane HTTP surface.
func New(a *app.App) *API {
	bus := NewBus(a)
	return &API{app: a, bus: bus, mcp: NewMCP(bus)}
}

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
	res, err := s.bus.Dispatch(r.Context(), name, args, DoorUI, callerOf(r))
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
	switch action {
	case "apply":
		extra := Args{}
		if raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)); len(strings.TrimSpace(string(raw))) > 0 {
			_ = json.Unmarshal(raw, &extra)
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
