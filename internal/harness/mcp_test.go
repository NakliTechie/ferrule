package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"io"
	"regexp"

	"ferrule/internal/api"
	"ferrule/internal/app"
	"ferrule/internal/discovery"
	"ferrule/internal/mock"
	"ferrule/internal/server"
	"ferrule/internal/store"
	"ferrule/internal/ui"
)

// mcpCall drives the agent face the way a client does: through the manifest, over the
// MCP endpoint, never through the DOM (§4.10.6).
func (r *rig) mcpCall(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ferrule-Caller", "harness-agent")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if e, ok := doc["error"]; ok {
		t.Fatalf("%s: %v", method, e)
	}
	res, _ := doc["result"].(map[string]any)
	return res
}

func structured(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	if res["isError"] == true {
		content := res["content"].([]any)[0].(map[string]any)
		t.Fatalf("tool call failed: %v", content["text"])
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent in %v", res)
	}
	return sc
}

// Checkpoint 6 (§4.10.6): an MCP client calls list_models, set_alias, and usage_summary
// through the manifest; set_alias stages and requires an explicit apply; every call is
// recorded with door and caller; the parity lint (manifest ⊇ command bus) passes.
func TestCheckpointMCPControlFace(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b", "nomic-embed-text")
	defer up.Close()
	src := r.addLocal(t, "ollama", up)

	// The manifest is the interface. Everything below goes through it.
	manifest := r.mcpCall(t, "tools/list", map[string]any{})
	tools := manifest["tools"].([]any)
	byName := map[string]map[string]any{}
	for _, x := range tools {
		tool := x.(map[string]any)
		byName[tool["name"].(string)] = tool
	}
	for _, want := range []string{"list_sources", "list_models", "get_alias", "usage_summary",
		"add_source", "set_alias", "revoke_grant"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("the manifest omits %q", want)
		}
	}

	// A read op answers immediately.
	res := structured(t, r.mcpCall(t, "tools/call", map[string]any{
		"name": "list_models", "arguments": map[string]any{"where": "local"},
	}))
	models := res["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("list_models returned %d models, want 2", len(models))
	}

	// A mutating op stages; nothing lands.
	staged := structured(t, r.mcpCall(t, "tools/call", map[string]any{
		"name": "set_alias",
		"arguments": map[string]any{
			"name": "fast", "ladder": []any{src.ID + "/qwen3:8b"},
		},
	}))
	if staged["staged"] != true {
		t.Fatalf("set_alias did not stage: %v", staged)
	}
	stagedID, _ := staged["id"].(string)
	if stagedID == "" {
		t.Fatal("staged op has no id to apply")
	}
	if _, err := r.app.DB.Alias("fast"); err == nil {
		t.Fatal("set_alias landed without a person applying it")
	}

	// The person applies it, and only then does it land.
	applyResp, err := http.DefaultClient.Do(
		r.controlReq(t, http.MethodPost, "/api/staged/"+stagedID+"/apply", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("apply: HTTP %d", applyResp.StatusCode)
	}
	al, err := r.app.DB.Alias("fast")
	if err != nil {
		t.Fatalf("alias did not land after apply: %v", err)
	}
	if len(al.Rungs) != 1 || al.Rungs[0].ModelID != "qwen3:8b" {
		t.Fatalf("alias landed as %+v", al.Rungs)
	}

	// usage_summary through the manifest.
	usage := structured(t, r.mcpCall(t, "tools/call", map[string]any{
		"name": "usage_summary", "arguments": map[string]any{"by": []any{"app"}},
	}))
	if _, ok := usage["total"]; !ok {
		t.Fatalf("usage_summary returned %v", usage)
	}

	// Every control call is recorded with its door and its caller.
	log, err := r.app.DB.ControlLog(50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, c := range log {
		seen[c.Op+"@"+c.Door] = c.Caller
	}
	for _, want := range []string{"list_models@mcp", "set_alias@mcp", "usage_summary@mcp"} {
		caller, ok := seen[want]
		if !ok {
			t.Errorf("%s was not recorded in the control log", want)
			continue
		}
		if caller != "harness-agent" {
			t.Errorf("%s recorded caller %q, want harness-agent", want, caller)
		}
	}
	if seen["set_alias@ui"] == "" {
		t.Error("the apply was not recorded against the person's door")
	}
}

// Parity lint (§4.7): the manifest is a superset of the command bus. The UI dispatches
// nothing the manifest omits, because both are generated from the same registry.
func TestParityManifestSupersetsCommandBus(t *testing.T) {
	r := newRig(t)
	manifest := r.mcpCall(t, "tools/list", map[string]any{})
	inManifest := map[string]bool{}
	for _, x := range manifest["tools"].([]any) {
		inManifest[x.(map[string]any)["name"].(string)] = true
	}
	for _, o := range r.bus.Ops() {
		if !inManifest[o.Name] {
			t.Errorf("command bus op %q is missing from the manifest", o.Name)
		}
	}
	// And every op the panel's JS dispatches exists on the bus.
	for _, name := range panelOps(t) {
		if _, ok := r.bus.Op(name); !ok {
			t.Errorf("the panel dispatches %q, which the command bus does not define", name)
		}
	}
}

// A person-only op is refused at the agent door, and says so rather than vanishing.
func TestPersonOnlyOpsAreNamedAndRefused(t *testing.T) {
	r := newRig(t)
	manifest := r.mcpCall(t, "tools/list", map[string]any{})
	var mint map[string]any
	for _, x := range manifest["tools"].([]any) {
		tool := x.(map[string]any)
		if tool["name"] == "mint_grant" {
			mint = tool
		}
	}
	if mint == nil {
		t.Fatal("mint_grant is absent from the manifest; a non-delegable act must be marked, not hidden")
	}
	ann := mint["annotations"].(map[string]any)
	if ann["personOnly"] != true {
		t.Errorf("mint_grant is not marked person-only: %v", ann)
	}

	res := r.mcpCall(t, "tools/call", map[string]any{
		"name": "mint_grant", "arguments": map[string]any{"app": "sneaky"},
	})
	if res["isError"] != true {
		t.Fatal("an agent minted a credential")
	}
	grants, _ := r.app.DB.Grants()
	if len(grants) != 0 {
		t.Fatalf("%d grants exist after a refused mint", len(grants))
	}
	log, _ := r.app.DB.ControlLog(10)
	var refused bool
	for _, c := range log {
		if c.Op == "mint_grant" && strings.HasPrefix(c.Outcome, "refused") {
			refused = true
		}
	}
	if !refused {
		t.Error("the refusal was not recorded in the control log")
	}
}

// The vault invariant survives staging: an agent's add_source cannot park a provider key
// in the staging table.
func TestStagedAddSourceWithholdsTheKey(t *testing.T) {
	r := newRig(t)
	res := structured(t, r.mcpCall(t, "tools/call", map[string]any{
		"name": "add_source",
		"arguments": map[string]any{
			"provider": "deepseek", "name": "deepseek", "key": "sk-agent-supplied-secret",
		},
	}))
	if res["staged"] != true {
		t.Fatalf("add_source did not stage: %v", res)
	}
	withheld := res["withheld"].([]any)
	if len(withheld) != 1 || withheld[0] != "key" {
		t.Fatalf("withheld %v, want [key]", withheld)
	}
	ops, err := r.app.DB.StagedOps()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.Payload, "sk-agent-supplied-secret") {
			t.Fatalf("the staged payload carries the key: %s", o.Payload)
		}
	}
}

// The control plane refuses to be driven by a random web page (§4.5). This is asserted
// against the assembled daemon, because the guard lives in the assembly.
func TestControlPlaneRejectsForeignOrigins(t *testing.T) {
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv, err := server.New(a, server.Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	base := "http://" + srv.Addr()

	// The token the panel would have been handed. A cross-origin caller cannot read the
	// page it is embedded in, so it cannot have this.
	page, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	m := regexp.MustCompile(`name="ferrule-control" content="([^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatal("the panel was served without a control token")
	}
	control := string(m[1])

	call := func(method, path, origin, token string) int {
		req, _ := http.NewRequest(method, base+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if token != "" {
			req.Header.Set(api.HeaderName, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// The attack that worked before this test existed: a browser fetches a URL with no
	// Origin header — an <img> tag, a <script> src, a navigation — and the daemon
	// executes a mutation. Both halves are now closed.
	if code := call(http.MethodGet, "/api/op/set_setting?key=cross_origin&value=on", "", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("a URL-only mutation got HTTP %d, want 405", code)
	}
	if code := call(http.MethodPost, "/api/op/set_setting", "", ""); code != http.StatusForbidden {
		t.Errorf("a POST mutation without the control token got HTTP %d, want 403", code)
	}
	if code := call(http.MethodPost, "/api/op/mint_grant", "https://evil.example", ""); code != http.StatusForbidden {
		t.Errorf("a foreign origin minted a credential: HTTP %d, want 403", code)
	}
	// A hostname that merely starts with "localhost" is not this machine.
	if code := call(http.MethodPost, "/api/op/status", "http://localhost.evil.example", ""); code != http.StatusForbidden {
		t.Errorf("localhost.evil.example got HTTP %d, want 403", code)
	}
	// Reads are guarded too: without the token, nothing on the control plane answers.
	if code := call(http.MethodPost, "/api/op/list_sources", "", ""); code != http.StatusForbidden {
		t.Errorf("an untokened read got HTTP %d, want 403", code)
	}
	// And the panel, which has the token, works.
	if code := call(http.MethodPost, "/api/op/status", "http://127.0.0.1:8899", control); code != http.StatusOK {
		t.Errorf("the panel got HTTP %d, want 200", code)
	}
	// The inference lane is exempt by design — SDKs send no Origin and authenticate with
	// an app token — but it must still refuse an unauthenticated caller.
	if code := call(http.MethodGet, "/v1/models", "https://evil.example", ""); code != http.StatusUnauthorized {
		t.Errorf("the inference lane got HTTP %d for an unauthenticated call, want 401", code)
	}
	// No grant was created by any of the above.
	grants, err := a.DB.Grants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("%d grant(s) exist after the attack sequence", len(grants))
	}
}

func panelJS(t *testing.T) string {
	t.Helper()
	raw, err := ui.Asset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// panelOps extracts every op name the embedded panel dispatches, so the parity claim is
// checked against the shipped asset rather than a list someone maintains by hand.
func panelOps(t *testing.T) []string {
	t.Helper()
	raw := panelJS(t)
	var out []string
	for _, chunk := range strings.Split(raw, `op("`)[1:] {
		if i := strings.Index(chunk, `"`); i > 0 {
			out = append(out, chunk[:i])
		}
	}
	if len(out) < 8 {
		t.Fatalf("only %d dispatched ops found in the panel; the extractor is broken", len(out))
	}
	return out
}

// A key must not reach the staging table by any route, including ones the op did not
// declare. An agent that passes it as `api_key`, or hides it in a base URL's query
// string, must not have Ferrule write it to SQLite in plaintext.
func TestStagingRefusesCredentialsByAnyRoute(t *testing.T) {
	r := newRig(t)
	const canary = "sk-agent-supplied-secret-canary"

	res := structured(t, r.mcpCall(t, "tools/call", map[string]any{
		"name": "add_source",
		"arguments": map[string]any{
			"provider": "deepseek",
			"name":     "deepseek",
			"key":      canary,                                      // declared secret
			"api_key":  canary,                                      // undeclared
			"base_url": "https://api.deepseek.com/v1?key=" + canary, // hidden in a URL
		},
	}))
	if res["staged"] != true {
		t.Fatalf("add_source did not stage: %v", res)
	}
	ops, err := r.app.DB.StagedOps()
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("nothing was staged")
	}
	for _, o := range ops {
		if strings.Contains(o.Payload, canary) {
			t.Fatalf("the staged payload carries the key: %s", o.Payload)
		}
	}
	if len(res["dropped_undeclared"].([]any)) == 0 {
		t.Error("the undeclared argument was neither staged nor reported as dropped")
	}
}

// Two applies racing on one staged operation must not both run it.
func TestStagedOpAppliesExactlyOnce(t *testing.T) {
	r := newRig(t)
	staged, err := r.app.DB.Stage("mint_grant", `{"app":"racer"}`, "mcp", "test")
	if err != nil {
		t.Fatal(err)
	}
	// mint_grant is person-only, so stage it directly and apply through the person's
	// door — which is the path a real apply takes.
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.bus.Apply(context.Background(), staged.ID, nil, api.DoorUI, "test"); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of 8 concurrent applies succeeded, want exactly 1", wins)
	}
	grants, err := r.app.DB.Grants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("%d grants minted from one staged operation", len(grants))
	}
}

// Ferrule on the home network: inference for the household, the vault for the machine it
// lives on.
//
// The daemon binds a real non-loopback address here and is driven over it, because the
// whole guard turns on the peer address of the accepted connection — a loopback test
// server cannot exercise it at all.
func TestOnTheNetworkInferenceIsSharedAndTheVaultIsNot(t *testing.T) {
	ip := lanAddr(t)

	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	up := mock.New("", "qwen3:8b")
	defer up.Close()
	if _, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "ollama", Provider: "ollama", BaseURL: up.BaseURL(),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := api.NewBus(a).Dispatch(context.Background(), "mint_grant",
		api.Args{"app": "om"}, api.DoorCLI, "test")
	if err != nil {
		t.Fatal(err)
	}
	tok := res.(map[string]any)["token"].(string)

	srv, err := server.New(a, server.Options{Addr: ip + ":0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	base := "http://" + srv.Addr()

	get := func(method, path, token string, body string) int {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// The family's half: inference works with a token, and only with one.
	if code := get(http.MethodGet, "/v1/models", tok, ""); code != http.StatusOK {
		t.Errorf("inference over the network got HTTP %d, want 200 — this is the point", code)
	}
	if code := get(http.MethodGet, "/v1/models", "", ""); code != http.StatusUnauthorized {
		t.Errorf("untokened inference got HTTP %d, want 401", code)
	}
	if code := get(http.MethodPost, "/p/replicate/predictions", tok, "{}"); code == http.StatusOK {
		t.Error("the passthrough mount served a source that does not exist")
	}

	// The machine's half: nothing that touches the vault answers the network, and an app
	// token does not buy any of it either.
	for _, path := range []string{
		"/", "/api/op/status", "/api/op/list_sources", "/api/op/mint_grant",
		"/api/op/export_config", "/api/op/read_content", "/mcp", "/api/staged/",
	} {
		if code := get(http.MethodPost, path, tok, "{}"); code != http.StatusForbidden {
			t.Errorf("%s answered the network with HTTP %d, want 403", path, code)
		}
	}

	// And the control token is not obtainable over the network, because the page that
	// carries it is not served over the network.
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(page, []byte("ferrule-control")) {
		t.Fatal("the control token was served to the network")
	}

	// The endpoint the panel hands out has to be one that actually answers. Asserting a
	// particular interface was wrong on a machine with two: it failed for the right
	// reason — LANEndpoint was naming an interface the listener was not bound to — and
	// the fix belongs in the product, so the test now asks the question the family asks.
	ep := srv.LANEndpoint()
	if ep == "" {
		t.Fatal("a network-bound daemon offered no address for other machines")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://"+ep+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	epResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the address the panel hands out does not answer: %s: %v", ep, err)
	}
	epResp.Body.Close()
	if epResp.StatusCode != http.StatusOK {
		t.Errorf("the address the panel hands out answered HTTP %d, want 200", epResp.StatusCode)
	}
}

// Sharing is a setting checked on the accepted connection, not a decision made when the
// listener was bound. That is what lets the panel turn it off and on with no restart —
// and the restart was the reason it was a flag, which is the reason nobody would ever
// have used it.
//
// Bound to a real network address here, because the guard turns entirely on the peer
// address of the accepted connection and a loopback server cannot exercise it.
func TestSharingIsASettingThatTakesEffectWithoutARestart(t *testing.T) {
	ip := lanAddr(t)
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	up := mock.New("", "qwen3:8b")
	defer up.Close()
	if _, err := a.Discovery.Add(context.Background(), discovery.AddRequest{
		Name: "ollama", Provider: "ollama", BaseURL: up.BaseURL(),
	}); err != nil {
		t.Fatal(err)
	}
	_, tok, err := a.HouseholdKey()
	if err != nil {
		t.Fatal(err)
	}

	// Every interface, which is the shipped default — the point of this test is that one
	// listener serves this machine and the network differently, by setting.
	srv, err := server.New(a, server.Options{Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	ask := func(host string) int {
		req, _ := http.NewRequest(http.MethodGet, "http://"+host+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET from %s: %v", host, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	_, port, _ := net.SplitHostPort(srv.Addr())
	network, here := net.JoinHostPort(ip, port), net.JoinHostPort("127.0.0.1", port)

	// On by default: a household appliance only its own machine can use is not one.
	if code := ask(network); code != http.StatusOK {
		t.Fatalf("sharing is not on by default: the network got HTTP %d, want 200", code)
	}

	if err := a.DB.SetSetting(store.SetSharing, "off"); err != nil {
		t.Fatal(err)
	}
	if code := ask(network); code != http.StatusForbidden {
		t.Errorf("sharing off served the network HTTP %d, want 403 — the toggle did not "+
			"take effect on the next request", code)
	}
	// Off means off for the network and nothing else. The machine Ferrule runs on keeps
	// working, or turning sharing off would break the person doing the turning.
	if code := ask(here); code != http.StatusOK {
		t.Errorf("sharing off broke this machine too: HTTP %d, want 200", code)
	}

	if err := a.DB.SetSetting(store.SetSharing, "on"); err != nil {
		t.Fatal(err)
	}
	if code := ask(network); code != http.StatusOK {
		t.Errorf("sharing back on served the network HTTP %d, want 200", code)
	}
}

// A narrow bind is absolute: no setting reopens a port that was never opened. This is the
// escape hatch for someone who wants the network to see nothing at all, and it has to be
// stronger than a row in a database the panel can write.
func TestANarrowBindCannotBeReopenedByASetting(t *testing.T) {
	ip := lanAddr(t)
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.DB.SetSetting(store.SetSharing, "on"); err != nil {
		t.Fatal(err)
	}

	srv, err := server.New(a, server.Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	if ep := srv.LANEndpoint(); ep != "" {
		t.Errorf("a loopback bind offered the network the address %q", ep)
	}
	_, port, _ := net.SplitHostPort(srv.Addr())
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 2*time.Second)
	if err == nil {
		conn.Close()
		t.Error("the network reached a listener bound to loopback, with sharing on")
	}
}

// Parity had two directions and was missing a third. The manifest supersets the bus, and
// every op the panel dispatches exists on the bus — but nothing checked that the panel
// offers the *inputs* those ops accept. `test_model` shipped on the bus and in the CLI
// and the panel's form never had a field for it, so an account whose tier excludes
// Ferrule's picks could be added from a terminal and not from the surface a person uses.
func TestThePanelOffersTheInputsItsOpsAccept(t *testing.T) {
	r := newRig(t)
	js := panelJS(t)

	// Deliberate omissions, each with the reason it is one. A param listed here is a
	// decision; a param missing from both lists is drift.
	except := map[string]string{
		// One click to send a key over plaintext http to another machine is not a thing
		// the panel should offer. The CLI makes you type -insecure; the panel refuses the
		// endpoint and says why.
		"add_source.allow_insecure": "an explicit security acknowledgement, CLI-only on purpose",
	}

	for _, name := range panelOps(t) {
		o, ok := r.bus.Op(name)
		if !ok {
			continue // covered by the parity test above
		}
		for _, p := range o.Params {
			if _, skip := except[name+"."+p.Name]; skip {
				continue
			}
			// Either the wire name, or the camelCase the panel uses when it holds the
			// input as local state and filters what it already has rather than asking
			// the server again. Both mean the person can express the thing.
			if !strings.Contains(js, p.Name) && !strings.Contains(js, camel(p.Name)) {
				t.Errorf("the panel dispatches %s but offers no %q; add a field, or list it "+
					"in this test's exceptions with the reason", name, p.Name)
			}
		}
	}
}

// camel turns a wire param name into the panel's local-state spelling: max_cost → maxCost.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// The cross-origin developer setting exists so a locally-developed web app can call the op
// API. Turning it on also opened the two control routes that carry no token of their own —
// /mcp and /api/manifest — to every origin, so a page in the person's browser could drive
// the agent door: read the source list, the ledger, the usage view, and stage operations.
//
// The op API was never actually reachable that way, because the token header is not in
// Access-Control-Allow-Headers. The routes with no token were.
func TestTheCrossOriginSettingDoesNotPublishTheAgentDoor(t *testing.T) {
	a, err := app.Open(app.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// The setting at its most permissive, which is the case that has to hold.
	if err := a.DB.SetSetting(store.SetCrossOrigin, "on"); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(a, server.Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	base := "http://" + srv.Addr()

	ask := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin")
	}

	// A route with no token of its own is never handed to another origin, whatever the
	// setting says.
	for _, path := range []string{"/mcp", "/api/manifest", "/"} {
		code, allow := ask(http.MethodPost, path, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if code != http.StatusForbidden {
			t.Errorf("%s answered a foreign origin HTTP %d, want 403", path, code)
		}
		if allow != "" {
			t.Errorf("%s sent Access-Control-Allow-Origin: %s", path, allow)
		}
	}

	// The route the setting is for still works, and still needs the token — which a
	// browser cannot send, because it is not an allowed header.
	code, allow := ask(http.MethodPost, "/api/op/status", "{}")
	if allow != "https://evil.example" {
		t.Errorf("the setting no longer relaxes the op API it exists for: allow=%q", allow)
	}
	if code == http.StatusOK {
		t.Errorf("the op API answered without the control token: HTTP %d", code)
	}
}
