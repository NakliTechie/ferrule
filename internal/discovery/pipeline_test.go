package discovery_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/discovery"
	"github.com/NakliTechie/ferrule/internal/mock"
	"github.com/NakliTechie/ferrule/internal/provider"
	"github.com/NakliTechie/ferrule/internal/store"
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
		{"nvidia", "nvapi-harness", []string{"meta/llama-3.3-70b-instruct"}},
		{"openai", "sk-harness", []string{"gpt-4o"}},
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
		if c.provider == "replicate" && r.Source.Lane != store.LanePassthrough {
			t.Errorf("replicate: lane %q, want passthrough", r.Source.Lane)
		}
		// Every seed source, Replicate included, must reach live with at least one
		// classified model. Exempting one provider from the checkpoint's own bar is how
		// a source that is live and empty — routable by nothing, explicable by no
		// interface — gets to pass as working.
		assertLive(t, a, r)
		if c.provider != "replicate" && r.Source.Kind != store.KindCloud {
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

// Every seeded provider must be exercised by the pipeline checkpoint. Adding one to the
// curated set and not to the harness is how a provider ships untested.
func TestEverySeededProviderIsCoveredByTheCheckpoint(t *testing.T) {
	// The providers the checkpoint stands up, kept beside the table it uses.
	covered := map[string]bool{
		"ollama": true, "lmstudio": true, "llamacpp": true, // detected
		"anthropic": true, "deepseek": true, "groq": true, "nvidia": true,
		"openai": true, "replicate": true,
		"openai-compatible": true, // asserted by TestUnknownProviderIsRefusedLoudly
	}
	for _, spec := range provider.All() {
		if !covered[spec.ID] {
			t.Errorf("provider %q is seeded but no checkpoint case exercises it", spec.ID)
		}
	}
}

// A local runtime that is slow to answer its first request must still be detected.
//
// This is the regression for a real failure: Ollama takes well over a second to serve
// its first /v1/models in a cold process and about twenty milliseconds thereafter. A
// single short detection budget caught the warm case and missed the cold one, so a
// runtime that was plainly running reported as "not detected" — intermittently, which is
// the worst way to be wrong about the one feature the product leads with.
func TestSlowToWakeRuntimeIsStillDetected(t *testing.T) {
	a := newApp(t)

	var once sync.Once
	slow := mock.New("", "qwen3:8b")
	defer slow.Close()
	base := slow.Server.URL
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first request pays the wake-up cost, well past any sub-second budget.
		once.Do(func() { time.Sleep(1500 * time.Millisecond) })
		proxy, _ := http.NewRequest(r.Method, base+r.URL.Path, r.Body)
		proxy.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(proxy)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer gate.Close()

	a.Discovery.SetDetectURLs("ollama", []string{gate.URL + "/v1"})
	a.Discovery.SetDetectURLs("lmstudio", nil)
	a.Discovery.SetDetectURLs("llamacpp", nil)

	res, err := a.Discovery.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("detected %d runtimes, want 1 — a slow first response is not an absent runtime", len(res))
	}
	if res[0].Source.Status != store.StatusLive {
		t.Fatalf("status %q: %s", res[0].Source.Status, res[0].Reason.Message)
	}
}

// A scan of dead ports must stay fast: the connect probe is what keeps it so.
func TestScanningDeadPortsIsFast(t *testing.T) {
	a := newApp(t)
	dead := []string{
		"http://127.0.0.1:9/v1", "http://127.0.0.1:10/v1", "http://127.0.0.1:11/v1",
	}
	a.Discovery.SetDetectURLs("ollama", dead)
	a.Discovery.SetDetectURLs("lmstudio", dead)
	a.Discovery.SetDetectURLs("llamacpp", dead)

	start := time.Now()
	res, err := a.Discovery.Detect(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("detected %d runtimes on dead ports", len(res))
	}
	if elapsed > time.Second {
		t.Errorf("scanning 9 dead ports took %v; a closed port answers in milliseconds", elapsed)
	}
}

// A base URL is stored in SQLite and travels in a configuration export, so it must never
// become a place to hide a credential. The vault is the only place one belongs.
func TestCredentialsCannotRideInABaseURL(t *testing.T) {
	a := newApp(t)
	for _, u := range []string{
		"https://user:sk-secret@api.example.com/v1",
		"https://api.example.com/v1?api_key=sk-secret",
		"https://api.example.com/v1?access_token=abc123",
	} {
		_, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "x", Provider: "openai-compatible", BaseURL: u, Key: "sk-real-enough-to-store",
		})
		if err == nil {
			t.Errorf("%s was accepted", u)
			continue
		}
		r, ok := err.(discovery.Reason)
		if !ok || r.Code != discovery.CodeCredentialInURL {
			t.Errorf("%s refused as %v, want credential_in_url", u, err)
		}
	}
	// Nothing was written.
	srcs, err := a.DB.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 0 {
		t.Fatalf("%d source(s) persisted from a refused add", len(srcs))
	}
}

// Two runtimes of the same kind on different ports are two sources. Naming both after the
// provider meant the second silently replaced the first, and which one survived depended
// on which goroutine finished first.
func TestTwoRuntimesOfOneKindDoNotCollide(t *testing.T) {
	a := newApp(t)
	first := mock.New("", "qwen3:8b")
	defer first.Close()
	second := mock.New("", "gemma3:12b")
	defer second.Close()
	a.Discovery.SetDetectURLs("llamacpp", []string{first.BaseURL(), second.BaseURL()})
	a.Discovery.SetDetectURLs("ollama", nil)
	a.Discovery.SetDetectURLs("lmstudio", nil)

	res, err := a.Discovery.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("detected %d sources from two runtimes, want 2", len(res))
	}
	srcs, err := a.DB.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 {
		t.Fatalf("%d source(s) persisted; the second overwrote the first", len(srcs))
	}
	names := map[string]bool{}
	for _, s := range srcs {
		if names[s.Name] {
			t.Errorf("two sources share the name %q", s.Name)
		}
		names[s.Name] = true
	}
	// Both runtimes' models are reachable.
	models, err := a.DB.Models("")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Errorf("%d models across two runtimes, want 2", len(models))
	}
}

// The two failures real providers produced on first contact, replayed as fixtures.
//
// Both keys were valid — each provider listed the account's models with the key attached
// before refusing the test request. A single `test_failed` sent the person to check a key
// that was demonstrably fine, and in one case condemned an account with eighty working
// models on a sample of one.
func TestARefusalIsNamedByWhatTheProviderActuallySaid(t *testing.T) {
	t.Run("no balance stops immediately and says the key is fine", func(t *testing.T) {
		a := newApp(t)
		// DeepSeek, verbatim.
		up := mock.New("sk-real-enough-to-store", "deepseek-chat", "deepseek-reasoner")
		up.RefuseChat = map[string]mock.Refusal{"": {
			Status: 402,
			Body:   `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`,
		}}
		defer up.Close()

		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "deepseek", Provider: "deepseek", BaseURL: up.BaseURL(), Key: "sk-real-enough-to-store",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Reason.Code != discovery.CodeNoBalance {
			t.Fatalf("code %q, want no_balance", r.Reason.Code)
		}
		if !strings.Contains(r.Reason.Remedy, "key works") {
			t.Errorf("the remedy does not say the key is fine: %q", r.Reason.Remedy)
		}
		if strings.Contains(r.Reason.Remedy, "scope") {
			t.Errorf("the remedy still sends the person to check their key: %q", r.Reason.Remedy)
		}
		// An account-level refusal applies to every model, so it must not spend requests
		// discovering that twice.
		chats := 0
		for _, req := range up.Requests() {
			if strings.Contains(req.Path, "chat/completions") {
				chats++
			}
		}
		if chats != 1 {
			t.Errorf("%d chat attempts for an account-level refusal, want 1", chats)
		}
	})

	t.Run("one model's 404 does not condemn the account", func(t *testing.T) {
		a := newApp(t)
		// NVIDIA: most models outside the tier, one inside it.
		up := mock.New("nvapi-real", "meta/llama-3.1-405b", "meta/llama-3.1-70b",
			"meta/llama-3.1-8b-instruct")
		up.RefuseChat = map[string]mock.Refusal{
			"meta/llama-3.1-405b": {Status: 404, Body: `{"status":404,"title":"Not Found","detail":"Function not found for account"}`},
			"meta/llama-3.1-70b":  {Status: 404, Body: `{"status":404,"title":"Not Found","detail":"Function not found for account"}`},
		}
		defer up.Close()

		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Source.Status != store.StatusLive {
			t.Fatalf("an account with a working model was marked %q: %s",
				r.Source.Status, r.Reason.Message)
		}
		if len(r.Models) != 3 {
			t.Errorf("%d models kept, want all 3", len(r.Models))
		}
	})

	t.Run("every model refused says so, and the cost does not scale with the catalogue", func(t *testing.T) {
		// The real account listed 81 models. Whatever Ferrule spends discovering that
		// none of them work must not grow with that number, or adding a large provider
		// on a restricted tier becomes an expensive way to be told no.
		attempts := func(n int) (discovery.Reason, int) {
			a := newApp(t)
			models := make([]string, n)
			for i := range models {
				models[i] = fmt.Sprintf("a/model-%02d", i)
			}
			up := mock.New("nvapi-real", models...)
			up.RefuseChat = map[string]mock.Refusal{"": {
				Status: 404, Body: `{"status":404,"title":"Not Found","detail":"Function not found for account"}`,
			}}
			defer up.Close()
			r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
				Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for _, req := range up.Requests() {
				if strings.Contains(req.Path, "chat/completions") ||
					strings.Contains(req.Path, "embeddings") {
					calls++
				}
			}
			return r.Reason, calls
		}

		reason, small := attempts(5)
		if reason.Code != discovery.CodeModelUnavailable {
			t.Fatalf("code %q, want model_unavailable", reason.Code)
		}
		if !strings.Contains(reason.Remedy, "--test-model") {
			t.Errorf("the remedy does not offer the way out: %q", reason.Remedy)
		}
		// The ceiling is a constant: at most maxLiveProbes models classified (two calls
		// each) plus maxTestModels tested. What must never happen is a cost that follows
		// the catalogue.
		const ceiling = 20
		_, large := attempts(40)
		if small > ceiling || large > ceiling {
			t.Errorf("5 models cost %d requests and 40 cost %d; the ceiling is %d",
				small, large, ceiling)
		}
		if large-small > 4 {
			t.Errorf("eight times the catalogue cost %d more requests (%d vs %d) — the "+
				"attempt count is following the model list", large-small, large, small)
		}
	})

	t.Run("--test-model names the one the account can call", func(t *testing.T) {
		a := newApp(t)
		up := mock.New("nvapi-real", "a/one", "a/two", "a/works")
		up.RefuseChat = map[string]mock.Refusal{
			"a/one": {Status: 404, Body: `{"detail":"not found"}`},
			"a/two": {Status: 404, Body: `{"detail":"not found"}`},
		}
		defer up.Close()

		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
			TestModel: "a/works",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Source.Status != store.StatusLive {
			t.Fatalf("status %q: %s", r.Source.Status, r.Reason.Message)
		}
		// The model the person named is the one that proved the source. Classification
		// still probes other ids for their capabilities — that is a different question —
		// so what matters is that the test itself did not guess past the instruction.
		var lastChat string
		for _, req := range up.Requests() {
			if strings.Contains(req.Path, "chat/completions") {
				lastChat = req.Body
			}
		}
		if !strings.Contains(lastChat, `"a/works"`) {
			t.Errorf("the test did not use the named model; last chat was %s", lastChat)
		}
	})

	// A genuinely bad key is still a bad key, and still stops at once.
	t.Run("a bad key is unchanged", func(t *testing.T) {
		a := newApp(t)
		up := mock.New("sk-the-real-one", "deepseek-chat")
		defer up.Close()
		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "deepseek", Provider: "deepseek", BaseURL: up.BaseURL(), Key: "sk-a-typo",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Reason.Code != discovery.CodeBadKey {
			t.Fatalf("code %q, want bad_key", r.Reason.Code)
		}
	})
}

// A model the provider has retired answers 410, not 404. It is still a fact about that
// model id and not about the account — and when the caller named the model themselves,
// the remedy that sends them to --test-model is the remedy telling them to do what they
// just did. Both halves come from a real NVIDIA answer.
func TestARetiredModelIsAModelProblemAndTheRemedyKnowsWhoChoseIt(t *testing.T) {
	const retired = `{"type":"about:blank","title":"Gone","status":410,` +
		`"detail":"The model 'meta/llama-3.1-8b-instruct' has reached its end of life on ` +
		`2026-08-26T09:00:00Z and is no longer available."}`

	t.Run("the caller named it", func(t *testing.T) {
		a := newApp(t)
		up := mock.New("nvapi-real", "meta/llama-3.1-8b-instruct", "meta/llama-3.3-70b")
		up.RefuseChat = map[string]mock.Refusal{"": {Status: 410, Body: retired}}
		defer up.Close()

		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
			TestModel: "meta/llama-3.1-8b-instruct",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Reason.Code != discovery.CodeModelUnavailable {
			t.Fatalf("code %q, want model_unavailable — a 410 is about the model", r.Reason.Code)
		}
		if !strings.Contains(r.Reason.Remedy, "meta/llama-3.1-8b-instruct") {
			t.Errorf("the remedy does not name the model the caller chose: %q", r.Reason.Remedy)
		}
		if strings.Contains(r.Reason.Remedy, "check your tier, your quota") {
			t.Errorf("the remedy still blames the account: %q", r.Reason.Remedy)
		}
		// One named model means one attempt: guessing past the caller's choice spends
		// requests to learn something they already told Ferrule.
		chats := 0
		for _, req := range up.Requests() {
			if strings.Contains(req.Path, "chat/completions") {
				chats++
			}
		}
		if chats != 1 {
			t.Errorf("%d attempts for one named model, want 1", chats)
		}
	})

	t.Run("Ferrule picked it", func(t *testing.T) {
		a := newApp(t)
		up := mock.New("nvapi-real", "a/retired", "a/works")
		up.RefuseChat = map[string]mock.Refusal{"a/retired": {Status: 410, Body: retired}}
		defer up.Close()

		r, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
			Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Source.Status != store.StatusLive {
			t.Fatalf("one retired model marked the source %q: %s", r.Source.Status, r.Reason.Message)
		}
	})
}

// The one that cost a working account: adding a source that already exists is a replace,
// and a failed replace used to demolish what it was replacing. A live NVIDIA with 81
// models went dark because a later invocation named a model the provider had retired.
func TestAFailedReplaceLeavesTheLiveSourceStanding(t *testing.T) {
	a := newApp(t)
	up := mock.New("nvapi-real", "a/works", "a/also-works")
	defer up.Close()

	first, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLive(t, a, first)

	// The same experiment the real account was lost to.
	up.RefuseChat = map[string]mock.Refusal{"": {Status: 410, Body: `{"status":410,"title":"Gone"}`}}
	second, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
		TestModel: "a/retired",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Reason.OK() {
		t.Fatal("the failed attempt reported success")
	}
	if !second.Kept {
		t.Error("the result does not say the previous source was kept")
	}

	got, err := a.DB.SourceByName("nvidia")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusLive {
		t.Fatalf("a failed replace took the source down: status %q, reason %q",
			got.Status, got.StatusReason)
	}
	if got.TestModel != "" {
		t.Errorf("the failed attempt's test model %q stuck to the standing source", got.TestModel)
	}
	models, err := a.DB.Models(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Errorf("%d models survived, want 2", len(models))
	}
	key, err := a.Vault.Get(got.KeyRef)
	if err != nil || key != "nvapi-real" {
		t.Errorf("the key did not survive the failed replace: %q (%v)", key, err)
	}
}

// A source that needed --test-model to go live needs it again every time it is checked.
// Holding it in memory made the add work and the first refresh fail.
func TestTheTestModelSurvivesIntoRefresh(t *testing.T) {
	a := newApp(t)
	up := mock.New("nvapi-real", "a/outside-tier", "a/inside-tier")
	up.RefuseChat = map[string]mock.Refusal{
		"a/outside-tier": {Status: 404, Body: `{"status":404,"title":"Not Found"}`},
	}
	defer up.Close()

	added, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "nvidia", Provider: "nvidia", BaseURL: up.BaseURL(), Key: "nvapi-real",
		TestModel: "a/inside-tier",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLive(t, a, added)

	// Refreshed through a second process, which is the case that matters: the daemon that
	// re-checks this source next week is not the one that added it, so an answer held in
	// the adding engine's memory is an answer that is gone.
	b, err := app.Open(app.Options{Dir: a.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	stored, err := b.DB.SourceByName("nvidia")
	if err != nil {
		t.Fatal(err)
	}
	if stored.TestModel != "a/inside-tier" {
		t.Fatalf("the stored source does not remember its test model: %q", stored.TestModel)
	}
	refreshed, err := b.Discovery.Refresh(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.Reason.OK() {
		t.Fatalf("the refresh forgot which model this account can call: %s", refreshed.Reason.Message)
	}
}
