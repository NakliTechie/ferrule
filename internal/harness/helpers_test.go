package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ferrule/internal/api"
	"ferrule/internal/app"
	"ferrule/internal/discovery"
	"ferrule/internal/mock"
	"ferrule/internal/passthrough"
	"ferrule/internal/router"
	"ferrule/internal/store"
	"ferrule/internal/ui"
)

// rig is a whole Ferrule in memory: core, mounted endpoints, and a client.
type rig struct {
	app  *app.App
	srv  *httptest.Server
	bus  *api.Bus
	apiH *api.API
}

func newRig(t *testing.T) *rig {
	t.Helper()
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mux := http.NewServeMux()
	router.New(a.DB, a.Vault, a.Catalog).Mount(mux)
	passthrough.New(a.DB, a.Vault).Mount(mux)
	apiH := api.New(a)
	apiH.Mount(mux)
	ui.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { srv.Close(); a.Close() })
	return &rig{app: a, srv: srv, bus: apiH.Bus(), apiH: apiH}
}

// addSource runs the real pipeline against a mock upstream.
func (r *rig) addSource(t *testing.T, name, providerID, key string, up *mock.Provider) store.Source {
	t.Helper()
	res, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: name, Provider: providerID, BaseURL: up.BaseURL(), Key: key,
	})
	if err != nil {
		t.Fatalf("%s: add: %v", name, err)
	}
	if res.Source.Status != store.StatusLive {
		t.Fatalf("%s: %s", name, res.Reason.Message)
	}
	return res.Source
}

// addLocal registers a source that is genuinely on this machine, so the egress
// classifier has something honest to call local.
func (r *rig) addLocal(t *testing.T, name string, up *mock.Provider) store.Source {
	t.Helper()
	return r.addSource(t, name, "ollama", "", up)
}

func (r *rig) mint(t *testing.T, appName string) string {
	t.Helper()
	res, err := r.bus.Dispatch(context.Background(), "mint_grant", api.Args{"app": appName}, api.DoorCLI, "test")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	m := res.(map[string]any)
	return m["token"].(string)
}

// chat is an OpenAI-shaped chat call through Ferrule, exactly as an SDK would make it.
func (r *rig) chat(t *testing.T, token, model string, stream bool) (*http.Response, []byte) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "route me"}},
		"stream":   stream,
	})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func (r *rig) opJSON(t *testing.T, name string, args api.Args) map[string]any {
	t.Helper()
	res, err := r.bus.Dispatch(context.Background(), name, args, api.DoorCLI, "test")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(res)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
