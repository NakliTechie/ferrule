package harness_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NakliTechie/ferrule/internal/discovery"
	"github.com/NakliTechie/ferrule/internal/mock"
	"github.com/NakliTechie/ferrule/internal/store"
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
		AllowInsecure: true, // the fixture is http on the LAN interface, deliberately
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
	//
	// Compared as bytes, not as parsed JSON. An earlier version of this test unmarshalled
	// both sides and re-marshalled them before comparing, which would have passed
	// happily while Ferrule reordered keys, reindented, or dropped whitespace — the exact
	// alterations this lane promises never to make.
	canned := []byte(mock.PredictionFixture)
	if !bytes.Equal(body, canned) {
		t.Fatalf("response body was altered:\n got %q\nwant %q", body, canned)
	}
	// And the headers the provider set arrived as it set them.
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type became %q", ct)
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
	up := offMachineMock(t, "r8_key_long_enough")
	defer up.Close()
	if _, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "replicate", Provider: "replicate", BaseURL: up.BaseURL(), Key: "r8_key_long_enough",
		AllowInsecure: true,
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

// A Ferrule app token must not become general authority over the provider account behind
// a passthrough source.
//
// The mount attaches the person's key to whatever comes through it, so without a scope an
// app granted "make me an image" could also read billing, list files, or delete a
// fine-tune. Each provider declares its inference surface; everything else is refused
// before the key is even fetched.
func TestPassthroughLendsTheKeyOnlyForInference(t *testing.T) {
	r := newRig(t)
	const providerKey = "r8_the_stored_token"
	up := offMachineMock(t, providerKey)
	defer up.Close()
	if _, err := r.app.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "replicate", Provider: "replicate", BaseURL: up.BaseURL(), Key: providerKey,
		AllowInsecure: true,
	}); err != nil {
		t.Fatal(err)
	}
	tok := r.mint(t, "image-tool")
	before := len(up.Requests())

	refused := []struct{ method, path string }{
		{http.MethodGet, "account"},           // whose account is this
		{http.MethodGet, "billing/invoices"},  // what have they spent
		{http.MethodDelete, "predictions/p1"}, // destructive, and not inference
		{http.MethodGet, "trainings"},         // a different product surface entirely
		{http.MethodPost, "webhooks"},         // would redirect the account's traffic
	}
	for _, c := range refused {
		req, _ := http.NewRequest(c.method, r.srv.URL+"/p/replicate/"+c.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s /%s got HTTP %d, want 403 — an app token is not account authority",
				c.method, c.path, resp.StatusCode)
		}
	}
	if after := up.Requests()[before:]; len(after) != 0 {
		t.Fatalf("the provider was contacted for a refused call: %+v", after)
	}

	// Inference itself still works.
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/p/replicate/predictions",
		strings.NewReader(`{"version":"x"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a prediction got HTTP %d; the scope refused inference", resp.StatusCode)
	}
}

// The passthrough lane lends a provider key to an arbitrary path, so the allowlist is the
// only thing standing between an app token and the account itself. It checked the first
// path segment and nothing else.
//
// `predictions/../account` passed: the check saw `predictions` and said inference, and the
// tail was joined onto the base URL and sent with the key attached. Ferrule answered the
// caller 403 — and had already forwarded the request. A provider normalises before
// routing, so what it served was /account.
func TestThePassthroughAllowlistCannotBeWalkedPast(t *testing.T) {
	r := newRig(t)
	const key = "r8_the_stored_token"
	up := mock.New(key, "black-forest-labs/flux-schnell")
	defer up.Close()
	r.addSource(t, "replicate", "replicate", key, up)
	tok := r.mint(t, "app")

	// Discovery legitimately calls /account to prove the key. Only what happens after the
	// source is live is this test's business.
	before := len(up.Requests())

	for _, tail := range []string{
		"account",
		"predictions/../account",
		"predictions/%2e%2e/account",
		"predictions/..%2faccount",
		"predictions%2f..%2faccount",
		"predictions/./../account",
		"predictions//../account",
		"trainings",
		"deployments/x",
	} {
		req, _ := http.NewRequest(http.MethodGet, r.srv.URL+"/p/replicate/"+tail, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%q: %v", tail, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%q answered HTTP %d, want a refusal", tail, resp.StatusCode)
		}
	}

	// The assertion that matters is not the status the caller saw. It is whether the key
	// was lent to a path outside the allowlist.
	for _, q := range up.Requests()[before:] {
		if !strings.Contains(q.Path, "/predictions") && !strings.Contains(q.Path, "/models") &&
			!strings.Contains(q.Path, "/collections") {
			t.Errorf("the provider key was sent to %s %s, which is not inference",
				q.Method, q.Path)
		}
		if strings.Contains(q.Path, "..") {
			t.Errorf("a dot segment reached the provider: %s — it normalises before routing",
				q.Path)
		}
	}
}
