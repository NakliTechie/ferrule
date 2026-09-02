package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ferrule/internal/app"
	"ferrule/internal/mock"
	"ferrule/internal/server"
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
	applyResp, err := http.Post(r.srv.URL+"/api/staged/"+stagedID+"/apply",
		"application/json", strings.NewReader("{}"))
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

	get := func(path, origin string) int {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/api/op/status", "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("a foreign origin got HTTP %d on the control plane, want 403", code)
	}
	if code := get("/api/op/status", "http://127.0.0.1:8899"); code != http.StatusOK {
		t.Errorf("the panel's own origin got HTTP %d, want 200", code)
	}
	if code := get("/api/op/status", ""); code != http.StatusOK {
		t.Errorf("a request with no Origin got HTTP %d, want 200", code)
	}
	// The inference lane is exempt by design: SDKs send no Origin and authenticate with
	// an app token. It must still refuse an unauthenticated caller.
	if code := get("/v1/models", "https://evil.example"); code != http.StatusUnauthorized {
		t.Errorf("the inference lane got HTTP %d for an unauthenticated call, want 401", code)
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
