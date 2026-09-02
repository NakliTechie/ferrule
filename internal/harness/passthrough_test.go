package harness_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ferrule/internal/discovery"
	"ferrule/internal/store"
)

// Checkpoint 5 (§4.10.5): a prediction runs end-to-end through the passthrough mount; the
// stored key is injected; request and response bytes are unaltered versus a canned
// fixture (minus the injected auth header); egress is logged as cloud, provider=replicate.
func TestCheckpointMediaPassthroughLane(t *testing.T) {
	r := newRig(t)

	const providerKey = "r8_the_stored_token"
	up := offMachineMock(t, providerKey)
	defer up.Close()

	res, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "replicate", Provider: "replicate", BaseURL: up.BaseURL(), Key: providerKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Status != store.StatusLive {
		t.Fatalf("replicate: %s", res.Reason.Message)
	}
	if res.Source.Lane != store.LanePassthrough {
		t.Fatalf("lane %q, want passthrough", res.Source.Lane)
	}

	tok := r.mint(t, "image-tool")

	// The canned fixture: exactly the bytes a native Replicate client would send.
	const fixture = `{"version":"black-forest-labs/flux-schnell","input":{"prompt":"a ferrule, macro","num_outputs":1}}`

	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/p/replicate/predictions",
		strings.NewReader(fixture))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "wait") // a provider-specific header must survive untouched
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prediction: HTTP %d: %s", resp.StatusCode, body)
	}

	// Request bytes reached the provider unaltered.
	reqs := up.Requests()
	var seen *struct {
		Method string
		Path   string
		Auth   string
		Body   string
		Header http.Header
	}
	for i := range reqs {
		if reqs[i].Path == "/v1/predictions" {
			seen = &struct {
				Method string
				Path   string
				Auth   string
				Body   string
				Header http.Header
			}{reqs[i].Method, reqs[i].Path, reqs[i].Auth, reqs[i].Body, reqs[i].Header}
		}
	}
	if seen == nil {
		t.Fatal("the provider never saw the prediction call")
	}
	if seen.Body != fixture {
		t.Fatalf("request body was altered:\n got %s\nwant %s", seen.Body, fixture)
	}
	if seen.Method != http.MethodPost {
		t.Errorf("method %q, want POST", seen.Method)
	}
	if seen.Header.Get("Prefer") != "wait" {
		t.Errorf("provider-specific header dropped: Prefer=%q", seen.Header.Get("Prefer"))
	}
	// The injected auth header is the one difference, and it carries the stored key —
	// never the Ferrule app token.
	if seen.Auth != "Bearer "+providerKey {
		t.Fatalf("Authorization was %q, want the stored provider key", seen.Auth)
	}
	if strings.Contains(seen.Auth, tok) {
		t.Fatal("the Ferrule app token leaked upstream")
	}

	// Response bytes reached the caller unaltered.
	var got, want map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response is not the provider's JSON: %v", err)
	}
	canned := `{"id":"pred_harness_1","status":"succeeded","model":"black-forest-labs/flux-schnell","output":["https://example.invalid/out.webp"],"metrics":{"predict_time":1.25}}`
	if err := json.Unmarshal([]byte(canned), &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("response body was altered:\n got %s\nwant %s", gotJSON, wantJSON)
	}

	// The ledger records it as a passthrough call that went off-machine.
	entries, err := r.app.DB.Entries(10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Lane != store.LanePassthrough {
			continue
		}
		found = true
		if e.Provider != "replicate" {
			t.Errorf("provider %q, want replicate", e.Provider)
		}
		if e.Egress != store.EgressCloud {
			t.Errorf("egress %q, want cloud", e.Egress)
		}
		if e.App != "image-tool" {
			t.Errorf("app %q, want image-tool", e.App)
		}
		if e.RespBytes == 0 {
			t.Error("response bytes not recorded")
		}
	}
	if !found {
		t.Fatal("no passthrough row in the ledger")
	}
}

// A media-lane model is not silently normalized into the unified endpoint (§4.8).
func TestPassthroughLaneIsNotServedByTheUnifiedEndpoint(t *testing.T) {
	r := newRig(t)
	up := offMachineMock(t, "r8_key")
	defer up.Close()
	if _, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "replicate", Provider: "replicate", BaseURL: up.BaseURL(), Key: "r8_key",
	}); err != nil {
		t.Fatal(err)
	}
	tok := r.mint(t, "confused-app")
	resp, raw := r.chat(t, tok, "black-forest-labs/flux-schnell", false)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("the unified endpoint served a passthrough model: %s", raw)
	}
	if !strings.Contains(string(raw), "/p/") {
		t.Errorf("the error does not point at the passthrough mount: %s", raw)
	}
}

// The passthrough mount is not an open relay: it needs a live app token like every other
// door.
func TestPassthroughRequiresAnAppToken(t *testing.T) {
	r := newRig(t)
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/p/replicate/predictions",
		strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, want 401", resp.StatusCode)
	}
}
