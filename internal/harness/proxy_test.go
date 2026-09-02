package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ferrule/internal/api"
	"ferrule/internal/mock"
	"ferrule/internal/store"
)

// Checkpoint 2 (§4.10.2): an OpenAI-shaped client with a Ferrule app token completes a
// chat call routed to a cloud model and to a local model; a ledger row is written per
// call with correct app-token attribution and a local/cloud egress flag; a revoked token
// is rejected 401.
func TestCheckpointRawTokensProxyAndGrants(t *testing.T) {
	r := newRig(t)

	cloudUp := mock.New("sk-cloud", "claude-sonnet-5")
	defer cloudUp.Close()
	localUp := mock.New("", "qwen3:8b")
	defer localUp.Close()
	r.addSource(t, "anthropic", "anthropic", "sk-cloud", cloudUp)
	r.addLocal(t, "ollama", localUp)

	tok := r.mint(t, "notes-app")

	// (a) a cloud model
	resp, raw := r.chat(t, tok, "claude-sonnet-5", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cloud chat: HTTP %d: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "ferrule routed this") {
		t.Fatalf("cloud chat: unexpected body: %s", raw)
	}

	// (b) a local model
	resp, raw = r.chat(t, tok, "qwen3:8b", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local chat: HTTP %d: %s", resp.StatusCode, raw)
	}

	entries, err := r.app.DB.Entries(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d ledger rows, want 2 (one per call)", len(entries))
	}
	byModel := map[string]store.Entry{}
	for _, e := range entries {
		byModel[e.ModelID] = e
		if e.App != "notes-app" {
			t.Errorf("%s: app %q, want notes-app", e.ModelID, e.App)
		}
		if e.GrantID == "" {
			t.Errorf("%s: no grant id recorded", e.ModelID)
		}
		if e.PromptTokens == 0 || e.CompletionTokens == 0 {
			t.Errorf("%s: usage not captured (%d/%d)", e.ModelID, e.PromptTokens, e.CompletionTokens)
		}
	}
	// Egress is asserted, not logged. Both fixtures are on loopback here, so both rows
	// must say local — and if the classifier ever started trusting the source's label
	// instead of the address it dialled, this is where it would show.
	for id, e := range byModel {
		if e.Egress != store.EgressLocal {
			t.Errorf("%s: egress %q, want local — both fixtures are on loopback", id, e.Egress)
		}
	}
	if byModel["claude-sonnet-5"].Cost <= 0 {
		t.Errorf("cloud call priced at %v, want a positive cost from the catalog",
			byModel["claude-sonnet-5"].Cost)
	}

	// A revoked token is rejected 401.
	grants, err := r.app.DB.Grants()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.app.DB.RevokeGrant(grants[0].ID); err != nil {
		t.Fatal(err)
	}
	resp, _ = r.chat(t, tok, "qwen3:8b", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: HTTP %d, want 401", resp.StatusCode)
	}
	// An unknown token is rejected too.
	resp, _ = r.chat(t, "frl_not-a-real-token", "qwen3:8b", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token: HTTP %d, want 401", resp.StatusCode)
	}
}

// An alias resolves to its ladder, and a dead rung is stepped over rather than raised.
func TestAliasLadderDegradesToTheNextRung(t *testing.T) {
	r := newRig(t)
	broken := mock.New("", "gpt-oss-20b")
	defer broken.Close()
	good := mock.New("", "qwen3:8b")
	defer good.Close()

	brokenSrc := r.addLocal(t, "ollama", broken)
	goodSrc := r.addSource(t, "lmstudio", "lmstudio", "", good)
	broken.Fail = true // it listed fine, then started failing — the realistic case

	if _, err := r.bus.Dispatch(context.Background(), "set_alias", api.Args{
		"name":   "fast",
		"ladder": []any{brokenSrc.ID + "/gpt-oss-20b", goodSrc.ID + "/qwen3:8b"},
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}

	tok := r.mint(t, "ladder-app")
	resp, raw := r.chat(t, tok, "fast", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alias chat: HTTP %d: %s", resp.StatusCode, raw)
	}
	entries, _ := r.app.DB.Entries(10)
	if len(entries) != 2 {
		t.Fatalf("%d ledger rows, want 2 — the failed rung must be recorded, not hidden", len(entries))
	}
	// Newest first: the successful second rung, then the failed first.
	if entries[0].ModelID != "qwen3:8b" || entries[0].Status != http.StatusOK {
		t.Errorf("winning rung recorded as %s/%d", entries[0].ModelID, entries[0].Status)
	}
	if entries[1].ModelID != "gpt-oss-20b" || entries[1].Err == "" {
		t.Errorf("failed rung recorded as %s/%q", entries[1].ModelID, entries[1].Err)
	}
}

// Remapping serves a hardcoded model id from whatever the person chose.
func TestRemapInterceptsAHardcodedModelID(t *testing.T) {
	r := newRig(t)
	local := mock.New("", "qwen3:8b")
	defer local.Close()
	src := r.addLocal(t, "ollama", local)

	if _, err := r.bus.Dispatch(context.Background(), "set_remap", api.Args{
		"from": "gpt-4o", "to": src.ID + "/qwen3:8b",
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	tok := r.mint(t, "stubborn-app")
	resp, raw := r.chat(t, tok, "gpt-4o", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remapped chat: HTTP %d: %s", resp.StatusCode, raw)
	}
	// The upstream must have been asked for the real model, not the remapped name.
	reqs := local.Requests()
	last := reqs[len(reqs)-1]
	if !strings.Contains(last.Body, `"model":"qwen3:8b"`) {
		t.Fatalf("upstream received %q, want the remapped target", last.Body)
	}
	entries, _ := r.app.DB.Entries(1)
	if entries[0].RequestedModel != "gpt-4o" || entries[0].ModelID != "qwen3:8b" {
		t.Errorf("ledger recorded %s→%s, want gpt-4o→qwen3:8b",
			entries[0].RequestedModel, entries[0].ModelID)
	}
}

// Streaming survives the proxy, and usage is picked out of the stream on the way past.
func TestStreamingIsRelayedAndAccounted(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "stream-app")

	resp, raw := r.chat(t, tok, "qwen3:8b", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("Content-Type %q, want an event stream", ct)
	}
	if !bytes.Contains(raw, []byte("data: ")) || !bytes.Contains(raw, []byte("[DONE]")) {
		t.Fatalf("stream body did not survive the proxy: %s", raw)
	}
	entries, _ := r.app.DB.Entries(1)
	if entries[0].PromptTokens != 11 || entries[0].CompletionTokens != 3 {
		t.Errorf("stream usage recorded as %d/%d, want 11/3",
			entries[0].PromptTokens, entries[0].CompletionTokens)
	}
}

// Ferrule's own /v1/models lists what it can serve, aliases included, so a client's
// model picker is truthful.
func TestFerruleListsWhatItCanServe(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b", "nomic-embed-text")
	defer up.Close()
	src := r.addLocal(t, "ollama", up)
	if _, err := r.bus.Dispatch(context.Background(), "set_alias", api.Args{
		"name": "local", "ladder": []any{src.ID + "/qwen3:8b"},
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	tok := r.mint(t, "picker")

	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range doc.Data {
		ids[d.ID] = true
	}
	for _, want := range []string{"qwen3:8b", "nomic-embed-text", "local"} {
		if !ids[want] {
			t.Errorf("/v1/models omits %q", want)
		}
	}
}

// An alias that exists but has no reachable rung must fail, not quietly resolve to
// something else with a similar name.
//
// This is the failure that would matter most: a person points an alias at their own
// machine, the runtime goes down, and the router helpfully finds a cloud model with the
// same id and sends the prompt there. Ferrule's entire claim is about where prompts go.
func TestExhaustedAliasDoesNotFallThroughToAnotherSource(t *testing.T) {
	r := newRig(t)

	cloud := mock.New("sk-cloud", "qwen3:8b") // the same model id, on a cloud source
	defer cloud.Close()
	local := mock.New("", "qwen3:8b")
	defer local.Close()

	r.addSource(t, "anthropic", "anthropic", "sk-cloud", cloud)
	localSrc := r.addLocal(t, "ollama", local)

	if _, err := r.bus.Dispatch(context.Background(), "set_alias", api.Args{
		"name": "qwen3:8b", "ladder": []any{localSrc.ID + "/qwen3:8b"},
	}, api.DoorCLI, "test"); err != nil {
		t.Fatal(err)
	}
	// The local source goes dark.
	if err := r.app.DB.SetSourceStatus(localSrc.ID, store.StatusFailed,
		"unreachable", "the runtime stopped", "start it again"); err != nil {
		t.Fatal(err)
	}

	// Everything the fixture itself sent is already on the record; only what happens
	// after this line is the router's doing.
	before := len(cloud.Requests())

	tok := r.mint(t, "careful-app")
	resp, raw := r.chat(t, tok, "qwen3:8b", false)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an exhausted alias was served from another source: %s", raw)
	}
	if !strings.Contains(string(raw), "qwen3:8b") {
		t.Errorf("the error does not name the alias that could not be served: %s", raw)
	}
	if after := cloud.Requests()[before:]; len(after) != 0 {
		t.Fatalf("a prompt reached the cloud source the alias never pointed at: %+v", after)
	}
}

// Streaming must be a relay, not a buffer.
//
// The earlier streaming test read the whole response and then asserted the bytes were
// there, which a proxy that quietly accumulated the entire completion and flushed it at
// the end would also pass. What matters to a person watching tokens appear is that each
// chunk leaves Ferrule when it arrives, so that is what is measured here.
func TestStreamingIsRelayedIncrementally(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	up.ChunkDelay = 120 * time.Millisecond
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "stream-app")

	lines, at := r.chatStream(t, tok, "qwen3:8b")
	if len(lines) < 4 {
		t.Fatalf("%d stream lines, want the chunks plus a terminator: %v", len(lines), lines)
	}
	if lines[len(lines)-1] != "data: [DONE]" {
		t.Errorf("stream did not end with [DONE]: %q", lines[len(lines)-1])
	}
	// The first chunk must arrive well before the last one was even produced upstream.
	firstToLast := at[len(at)-1] - at[0]
	if firstToLast < 200*time.Millisecond {
		t.Errorf("all %d chunks arrived within %v of each other; the proxy buffered the "+
			"stream instead of relaying it", len(lines), firstToLast)
	}
	if at[0] > 250*time.Millisecond {
		t.Errorf("the first chunk took %v to arrive; it should leave as soon as it lands", at[0])
	}
}
