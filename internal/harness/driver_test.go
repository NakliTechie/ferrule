package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
