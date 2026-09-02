package discovery_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferrule/internal/app"
	"ferrule/internal/discovery"
	"ferrule/internal/mock"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

func newApp(t *testing.T) *app.App {
	t.Helper()
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// Checkpoint 1 (FERRULE.md §4.10.1): every seed source reaches `live` with at least one
// classified model — local runtimes by detection, cloud providers by paste — and a
// deliberately-bad key reaches `failed` carrying a visible reason.
func TestCheckpointAddASourcePipeline(t *testing.T) {
	a := newApp(t)

	ollama := mock.New("", "qwen3:8b", "nomic-embed-text")
	defer ollama.Close()
	lmstudio := mock.New("", "llama-3.1-8b-instruct")
	defer lmstudio.Close()
	a.Discovery.SetDetectURLs("ollama", []string{ollama.BaseURL()})
	a.Discovery.SetDetectURLs("lmstudio", []string{lmstudio.BaseURL()})
	a.Discovery.SetDetectURLs("llamacpp", []string{"http://127.0.0.1:9/v1"}) // nothing listens on 9

	detected, err := a.Discovery.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(detected) != 2 {
		t.Fatalf("detected %d local sources, want 2", len(detected))
	}
	for _, r := range detected {
		assertLive(t, a, r)
		if !r.Source.Detected {
			t.Errorf("%s: detected flag not set", r.Source.Name)
		}
		if r.Source.Kind != store.KindLocal {
			t.Errorf("%s: kind %q, want local", r.Source.Name, r.Source.Kind)
		}
	}

	cloud := []struct {
		provider string
		key      string
		models   []string
	}{
		{"anthropic", "sk-ant-harness", []string{"claude-sonnet-5", "claude-haiku-4-5"}},
		{"deepseek", "sk-harness", []string{"deepseek-chat", "deepseek-reasoner"}},
		{"groq", "gsk_harness", []string{"llama-3.3-70b-versatile"}},
		{"replicate", "r8_harness", nil},
	}
	for _, c := range cloud {
		up := mock.New(c.key, c.models...)
		defer up.Close()
		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: c.provider, Provider: c.provider, BaseURL: up.BaseURL(), Key: c.key,
		})
		if err != nil {
			t.Fatalf("%s: add: %v", c.provider, err)
		}
		if r.Source.Status != store.StatusLive {
			t.Fatalf("%s: status %q (%s)", c.provider, r.Source.Status, r.Reason.Message)
		}
		if c.provider == "replicate" {
			// The passthrough lane is proven by an authenticated listing, and its models
			// come from the collection catalogue, not /v1/models.
			if r.Source.Lane != store.LanePassthrough {
				t.Errorf("replicate: lane %q, want passthrough", r.Source.Lane)
			}
			continue
		}
		assertLive(t, a, r)
		if r.Source.Kind != store.KindCloud {
			t.Errorf("%s: kind %q, want cloud", c.provider, r.Source.Kind)
		}
	}

	// A deliberately-bad key must fail loud, be persisted as failed, and carry a reason.
	bad := mock.New("gsk_the_real_one")
	defer bad.Close()
	r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "groq-typo", Provider: "groq", BaseURL: bad.BaseURL(), Key: "gsk_a_typo",
	})
	if err != nil {
		t.Fatalf("bad key: add returned a hard error instead of a failed source: %v", err)
	}
	if r.Source.Status != store.StatusFailed {
		t.Fatalf("bad key: status %q, want failed", r.Source.Status)
	}
	if strings.TrimSpace(r.Reason.Message) == "" {
		t.Fatal("bad key: failed with no visible reason")
	}
	if r.Reason.Code != discovery.CodeBadKey {
		t.Errorf("bad key: code %q, want %q", r.Reason.Code, discovery.CodeBadKey)
	}
	if !strings.Contains(r.Reason.Message, "401") {
		t.Errorf("bad key: message %q does not name the upstream status", r.Reason.Message)
	}
	if strings.TrimSpace(r.Reason.Remedy) == "" {
		t.Error("bad key: the reason names no remedy")
	}
	persisted, err := a.DB.Source(r.Source.ID)
	if err != nil {
		t.Fatalf("bad key: not persisted: %v", err)
	}
	if persisted.Status != store.StatusFailed || persisted.StatusReason == "" {
		t.Errorf("bad key: persisted as %q/%q, want failed with a reason", persisted.Status, persisted.StatusReason)
	}
}

func assertLive(t *testing.T, a *app.App, r discovery.Result) {
	t.Helper()
	if r.Source.Status != store.StatusLive {
		t.Fatalf("%s: status %q (%s)", r.Source.Name, r.Source.Status, r.Reason.Message)
	}
	models, err := a.DB.Models(r.Source.ID)
	if err != nil {
		t.Fatalf("%s: models: %v", r.Source.Name, err)
	}
	if len(models) == 0 {
		t.Fatalf("%s: live with zero models", r.Source.Name)
	}
	classified := 0
	for _, m := range models {
		if len(m.Capabilities) > 0 {
			classified++
		}
	}
	if classified == 0 {
		t.Fatalf("%s: %d model(s), none classified", r.Source.Name, len(models))
	}
}

// A local runtime that is up but empty must say so, not fail with a generic message and
// never by downloading a model uninvited.
func TestDetectedRuntimeWithNoModelsSaysSo(t *testing.T) {
	a := newApp(t)
	empty := mock.New("")
	defer empty.Close()
	a.Discovery.SetDetectURLs("ollama", []string{empty.BaseURL()})
	a.Discovery.SetDetectURLs("lmstudio", nil)
	a.Discovery.SetDetectURLs("llamacpp", nil)

	res, err := a.Discovery.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("detected %d, want 1", len(res))
	}
	if res[0].Source.Status != store.StatusFailed {
		t.Fatalf("status %q, want failed", res[0].Source.Status)
	}
	if res[0].Reason.Code != discovery.CodeLocalNoModels {
		t.Errorf("code %q, want %q", res[0].Reason.Code, discovery.CodeLocalNoModels)
	}
	if !strings.Contains(res[0].Reason.Remedy, "Pull a model") {
		t.Errorf("remedy %q does not tell the person what to do", res[0].Reason.Remedy)
	}
}

// The vault invariant (§4.5): a provider key never reaches SQLite, and never reaches the
// database file on disk in plaintext.
func TestKeyNeverReachesSQLite(t *testing.T) {
	dir := t.TempDir()
	a, err := app.Open(app.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sk-ant-ferrule-invariant-canary-9f3a"
	up := mock.New(secret, "claude-sonnet-5")
	defer up.Close()
	if _, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "anthropic", Provider: "anthropic", BaseURL: up.BaseURL(), Key: secret,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s contains the provider key in plaintext", e.Name())
		}
	}

	// And the vault still hands it back.
	a2, err := app.Open(app.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	src, err := a2.DB.SourceByName("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	got, err := a2.Vault.Get(src.KeyRef)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("vault returned %q, want the stored key", got)
	}
}

// The curated seed set is the product decision (§4.4); an unknown provider is refused
// with the known list rather than silently guessed at.
func TestUnknownProviderIsRefusedLoudly(t *testing.T) {
	a := newApp(t)
	_, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "x", Provider: "not-a-provider", Key: "k",
	})
	if err == nil {
		t.Fatal("unknown provider was accepted")
	}
	r, ok := err.(discovery.Reason)
	if !ok {
		t.Fatalf("unknown provider returned %T, want a typed Reason", err)
	}
	if r.Code != discovery.CodeUnknownProvider {
		t.Errorf("code %q, want %q", r.Code, discovery.CodeUnknownProvider)
	}
	if r.Remedy == "" {
		t.Error("the refusal names no remedy")
	}
	if _, ok := provider.Get("openai-compatible"); !ok {
		t.Error("the generic OpenAI-compatible provider is missing from the seed set")
	}
}
