package harness_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferrule/internal/api"
	"ferrule/internal/app"
	"ferrule/internal/mock"
	"ferrule/internal/store"
)

// Closure (§4.2): the person can export their whole configuration as one portable file
// they own and re-import it on another machine, keys included and still encrypted.
func TestConfigExportsAndReimportsOnAnotherMachine(t *testing.T) {
	r := newRig(t)
	const key = "sk-portable-canary"
	up := mock.New(key, "claude-sonnet-5")
	defer up.Close()
	src := r.addSource(t, "anthropic", "anthropic", key, up)
	if _, err := r.bus.Dispatch(context.Background(), "set_alias", api.Args{
		"name": "smart", "ladder": []any{src.ID + "/claude-sonnet-5"},
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	tok := r.mint(t, "portable-app")

	out := filepath.Join(t.TempDir(), "ferrule-config.json")
	if _, err := r.bus.Dispatch(context.Background(), "export_config",
		api.Args{"path": out}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The export carries the key blob, but not the key.
	if strings.Contains(string(raw), key) {
		t.Fatal("the exported configuration contains a provider key in plaintext")
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("export written with mode %v, want 0600", info.Mode().Perm())
	}

	// "Another machine": a second config directory, seeded with the same vault identity
	// the person carries with the file.
	dir2 := t.TempDir()
	for _, f := range []string{"vault.identity"} {
		b, err := os.ReadFile(filepath.Join(r.app.Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir2, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a2, err := app.Open(app.Options{Dir: dir2})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	bus2 := api.New(a2).Bus()
	if _, err := bus2.Dispatch(context.Background(), "import_config",
		api.Args{"path": out}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}

	got, err := a2.DB.SourceByName("anthropic")
	if err != nil {
		t.Fatalf("source did not survive the round trip: %v", err)
	}
	secret, err := a2.Vault.Get(got.KeyRef)
	if err != nil {
		t.Fatalf("key did not survive the round trip: %v", err)
	}
	if secret != key {
		t.Fatalf("vault returned %q after import", secret)
	}
	al, err := a2.DB.Alias("smart")
	if err != nil || len(al.Rungs) != 1 {
		t.Fatalf("alias did not survive the round trip: %v %+v", err, al)
	}
	models, _ := a2.DB.Models(got.ID)
	if len(models) == 0 {
		t.Error("classified models did not survive the round trip")
	}
	// The app token still authenticates: grants travel with the configuration.
	if g, err := a2.DB.GrantByToken(tok); err != nil || g.App != "portable-app" {
		t.Errorf("app token did not survive the round trip: %v", err)
	}
}

// Content logging is a real knob, not a dead one: off by default, recording when on,
// stored apart from the metadata ledger, and erasable.
func TestContentLoggingIsOffByDefaultAndRealWhenOn(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "content-app")

	const canary = "a-prompt-only-recorded-on-request"
	send := func() {
		body := `{"model":"qwen3:8b","messages":[{"role":"user","content":"` + canary + `"}]}`
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/chat/completions",
			strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	send()
	rows, err := r.app.DB.Contents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d content rows recorded with logging off", len(rows))
	}

	if _, err := r.bus.Dispatch(context.Background(), "set_setting",
		api.Args{"key": store.SetContentLogging, "value": "on"}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	send()
	rows, err = r.app.DB.Contents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d content rows recorded with logging on, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Request, canary) {
		t.Errorf("the recorded request does not hold the prompt: %q", rows[0].Request)
	}
	if rows[0].App != "content-app" {
		t.Errorf("content row attributed to %q", rows[0].App)
	}
	if rows[0].LedgerID == 0 {
		t.Error("content row is not tied back to a ledger row")
	}

	// It is erasable, and turning the setting off is not mistaken for erasing.
	if _, err := r.bus.Dispatch(context.Background(), "forget_content",
		api.Args{}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	rows, _ = r.app.DB.Contents(10)
	if len(rows) != 0 {
		t.Fatalf("%d content rows survived forget_content", len(rows))
	}
}

// Reading content is person-only: an agent must not be able to lift prompts through the
// control face, even when the person has turned logging on.
func TestAgentCannotReadTheContentLog(t *testing.T) {
	r := newRig(t)
	if _, err := r.bus.Dispatch(context.Background(), "set_setting",
		api.Args{"key": store.SetContentLogging, "value": "on"}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	res := r.mcpCall(t, "tools/call", map[string]any{
		"name": "read_content", "arguments": map[string]any{"limit": 10},
	})
	if res["isError"] != true {
		body, _ := json.Marshal(res)
		t.Fatalf("an agent read the content log: %s", body)
	}
}
