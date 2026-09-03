package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ferrule/internal/app"

	"ferrule/internal/api"
	"ferrule/internal/discovery"
	"ferrule/internal/mock"
	"ferrule/internal/store"
)

// The agent contract (SPEC.md §0). These are the properties an agent driving Ferrule is
// entitled to rely on, so they are asserted rather than described.

// §0.1 One perception act: a single bounded read renders the whole situation.
func TestBriefRendersTheWholeSituationInOneRead(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b", "nomic-embed-text")
	defer up.Close()
	src := r.addLocal(t, "ollama", up)
	dead := mock.New("gsk_right")
	dead.Close() // nothing is listening: this source will fail, loudly and typed
	if _, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "groq", Provider: "groq", BaseURL: dead.BaseURL(), Key: "gsk_wrong",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bus.Dispatch(context.Background(), "set_alias", api.Args{
		"name": "fast", "ladder": []any{src.ID + "/qwen3:8b"},
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}

	b := r.opJSON(t, "brief", api.Args{"endpoint": "http://localhost:8899/v1"})
	for _, key := range []string{"sources", "models", "aliases", "grants", "staged",
		"egress_24h", "failures", "recent_calls", "next", "reason_codes", "vault", "catalog"} {
		if _, ok := b[key]; !ok {
			t.Errorf("the brief omits %q — an agent would have to go looking", key)
		}
	}

	// Every dark source carries a code to branch on and a remedy to act on.
	var darkSeen bool
	for _, raw := range b["sources"].([]any) {
		s := raw.(map[string]any)
		if s["status"] == store.StatusLive {
			continue
		}
		darkSeen = true
		if s["code"] == "" || s["code"] == nil {
			t.Errorf("dark source %v carries no code", s["name"])
		}
		if s["remedy"] == "" || s["remedy"] == nil {
			t.Errorf("dark source %v names no remedy", s["name"])
		}
	}
	if !darkSeen {
		t.Fatal("the fixture's failing source did not fail")
	}

	// The suggested next moves include the dark source's remedy, delivered where it is read.
	next, _ := json.Marshal(b["next"])
	if !strings.Contains(string(next), "groq") {
		t.Errorf("the next moves do not mention the dark source: %s", next)
	}
	if !strings.Contains(string(next), "app token") {
		t.Errorf("with a live source and no grant, the next moves omit minting one: %s", next)
	}

	// Models are summarised, not listed: the brief must not grow with the catalog.
	models := b["models"].(map[string]any)
	if _, listed := models["models"]; listed {
		t.Error("the brief lists models; it must summarise them")
	}
	if int(models["servable"].(float64)) != 2 {
		t.Errorf("servable = %v, want 2", models["servable"])
	}
}

// §0.2 The reason vocabulary is closed, and the brief publishes it, so an agent can
// branch exhaustively rather than guess.
func TestReasonVocabularyIsClosedAndPublished(t *testing.T) {
	r := newRig(t)
	b := r.opJSON(t, "brief", api.Args{})
	published := map[string]bool{}
	for _, c := range b["reason_codes"].([]any) {
		published[c.(string)] = true
	}
	for _, c := range discovery.Codes() {
		if !published[string(c)] {
			t.Errorf("code %q is not published in the brief", c)
		}
	}
	if len(published) != len(discovery.Codes()) {
		t.Errorf("the brief publishes %d codes, the vocabulary has %d",
			len(published), len(discovery.Codes()))
	}
	// Every code that is not OK must carry a remedy: a verdict with no next action is
	// a verdict an agent cannot act on.
	for _, c := range discovery.Codes() {
		if c == discovery.CodeOK {
			continue
		}
		if discovery.RemedyFor(c) == "" {
			t.Errorf("code %q names no remedy", c)
		}
	}
}

// §0.3 Accretion: a classification a live probe paid for is not paid for twice.
func TestLiveProbeClassificationIsLearnedOnce(t *testing.T) {
	r := newRig(t)
	// An id the bundled catalog has nothing to say about, so classification falls to a
	// live probe.
	up := mock.New("", "zzz-unlisted-model-9000")
	defer up.Close()
	src := r.addLocal(t, "ollama", up)

	models, err := r.app.DB.Models(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || len(models[0].Capabilities) == 0 {
		t.Fatalf("live probe did not classify: %+v", models)
	}
	if r.app.DB.LearnedCount() != 1 {
		t.Fatalf("the probe's answer was not kept: %d learned", r.app.DB.LearnedCount())
	}

	before := len(up.Requests())
	if _, err := r.app.Discovery.Refresh(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}
	after := len(up.Requests())
	models, _ = r.app.DB.Models(src.ID)
	if len(models[0].Capabilities) == 0 {
		t.Fatal("the refreshed model lost its classification")
	}
	// A refresh costs a listing and the one test request. It must not re-probe an id
	// whose answer is already on disk.
	if spent := after - before; spent > 2 {
		t.Errorf("a refresh spent %d upstream requests; the learned answer was not used", spent)
	}
}

// §0.3 One verdict per distinct next action: the CLI's exit codes.
//
// This is asserted by running the binary, because the contract is about what a caller
// observes — and the bug it caught was a typed reason being flattened into a string on
// the way out, which changed a 2 into a 1 while every internal test still passed.
func TestExitCodesMapToNextActions(t *testing.T) {
	bin := buildFerrule(t)
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"an unknown verb is an invocation fault", []string{"nonsense"}, 2},
		{"an unknown provider is an invocation fault", []string{"add", "not-a-provider"}, 2},
		{"an unknown flag is an invocation fault", []string{"ls", "-nope"}, 2},
		{"a working command succeeds", []string{"version"}, 0},
		{"a read against an empty config succeeds", []string{"status"}, 0},
	}
	for _, c := range cases {
		cmd := exec.Command(bin, c.args...)
		cmd.Env = append(os.Environ(), "FERRULE_CONFIG_DIR="+dir)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if code != c.want {
			t.Errorf("%s: `ferrule %s` exited %d, want %d\n%s",
				c.name, strings.Join(c.args, " "), code, c.want, out)
		}
	}
}

// §0.4 Bounded output: the brief must not grow with history or with staging volume.
func TestBriefStaysBoundedAsHistoryGrows(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "noisy")

	small := len(mustJSON(t, r.opJSON(t, "brief", api.Args{})))
	for i := 0; i < 200; i++ {
		r.chat(t, tok, "qwen3:8b", false)
	}
	for i := 0; i < 50; i++ {
		if _, err := r.app.DB.Stage("set_alias", `{"name":"x"}`, "mcp", "flood"); err != nil {
			t.Fatal(err)
		}
	}
	big := len(mustJSON(t, r.opJSON(t, "brief", api.Args{})))

	// Some growth is legitimate — the egress totals gained digits. A brief that grew
	// with the ledger or the staging table would not be within a factor of two.
	if big > small*2 {
		t.Errorf("the brief grew from %d to %d bytes over 200 requests and 50 staged ops; "+
			"it is supposed to be bounded independently of both", small, big)
	}
}

// §0.6 Crash-safe: an interrupted write leaves the old state, never a hybrid.
func TestVaultSurvivesAnInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	a, err := app.Open(app.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Vault.Put("source:one", "sk-first"); err != nil {
		t.Fatal(err)
	}
	a.Close()

	// A crash mid-write leaves the temporary file behind and the real one untouched,
	// because the write is a rename, not an edit in place.
	if err := os.WriteFile(filepath.Join(dir, "vault.age.tmp"), []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	a2, err := app.Open(app.Options{Dir: dir})
	if err != nil {
		t.Fatalf("the vault did not survive a stray temporary file: %v", err)
	}
	defer a2.Close()
	got, err := a2.Vault.Get("source:one")
	if err != nil || got != "sk-first" {
		t.Fatalf("vault returned %q, %v after an interrupted write", got, err)
	}
}

// §0.6 Idempotent: adding the same source twice updates it rather than duplicating it.
func TestAddingTheSameSourceTwiceIsIdempotent(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	first := r.addLocal(t, "ollama", up)
	second := r.addLocal(t, "ollama", up)
	if first.ID != second.ID {
		t.Errorf("a second add created a new source (%s then %s)", first.ID, second.ID)
	}
	srcs, err := r.app.DB.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 {
		t.Fatalf("%d sources after adding the same one twice", len(srcs))
	}
}

func buildFerrule(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ferrule")
	cmd := exec.Command("go", "build", "-o", bin, "ferrule/cmd/ferrule")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building ferrule: %v\n%s", err, out)
	}
	return bin
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// §0.2 The vocabulary published in SPEC.md must be the one the code has.
//
// A hand-copied list in a document drifts the first time a code is added — which is what
// happened: `test_timeout` existed in code and not in the spec.
func TestSpecPublishesTheActualReasonVocabulary(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	block := regexp.MustCompile("(?s)```\\n(ok ·.*?)\\n```").FindStringSubmatch(spec)
	if block == nil {
		t.Fatal("SPEC.md no longer contains the reason-vocabulary block this test reads")
	}
	published := map[string]bool{}
	for _, f := range strings.Fields(strings.ReplaceAll(block[1], "·", " ")) {
		published[strings.TrimSpace(f)] = true
	}
	for _, c := range discovery.Codes() {
		if !published[string(c)] {
			t.Errorf("SPEC.md omits the reason code %q", c)
		}
	}
	if len(published) != len(discovery.Codes()) {
		t.Errorf("SPEC.md publishes %d codes, the code has %d", len(published), len(discovery.Codes()))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}

// A documented flag must take effect wherever it is written.
//
// Go's flag package stops at the first non-flag argument, so `ferrule add anthropic
// -base-url http://…` parsed no flags at all and silently used the provider's own
// endpoint. For -base-url that is a flag lying about where a credential is being sent,
// and it was invisible: no error, no warning, just the wrong destination.
func TestFlagsApplyOnEitherSideOfThePositional(t *testing.T) {
	bin := buildFerrule(t)

	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "FERRULE_CONFIG_DIR="+t.TempDir())
		cmd.Stdin = strings.NewReader("sk-ant-not-a-real-key\n")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	// A reserved .invalid name: not loopback, so the insecure-endpoint guard applies, and
	// it fails DNS immediately rather than hanging the way an unroutable IP does. If the
	// flag is honoured the guard refuses before any request; if it is ignored the run
	// reaches the provider's real endpoint instead.
	const host = "ferrule-nowhere.invalid"
	const url = "http://" + host + "/v1"
	for _, args := range [][]string{
		{"add", "anthropic", "-base-url", url},
		{"add", "-base-url", url, "anthropic"},
		{"add", "anthropic", "-base-url=" + url},
	} {
		out := run(args...)
		if !strings.Contains(out, host) {
			t.Errorf("`ferrule %s` did not use the base URL it was given:\n%s",
				strings.Join(args, " "), out)
		}
		if strings.Contains(out, "api.anthropic.com") {
			t.Errorf("`ferrule %s` sent the key to the provider's own endpoint instead:\n%s",
				strings.Join(args, " "), out)
		}
	}

	// A boolean flag after the positional must also register.
	out := run("add", "anthropic", "-base-url", url, "-insecure")
	if strings.Contains(out, "must be https") {
		t.Errorf("-insecure after the positional was ignored:\n%s", out)
	}
}
