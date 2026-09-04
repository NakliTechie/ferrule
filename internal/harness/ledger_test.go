package harness_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NakliTechie/ferrule/internal/mock"
	"github.com/NakliTechie/ferrule/internal/router"
	"github.com/NakliTechie/ferrule/internal/store"
)

// lanAddr finds a non-loopback address on this machine. A mock bound there is genuinely
// off-machine as far as the egress classifier is concerned — packets leave the loopback
// interface — without any request reaching the internet.
func lanAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("no interfaces: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		if ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		return ipnet.IP.String()
	}
	t.Skip("no non-loopback IPv4 address on this machine")
	return ""
}

func offMachineMock(t *testing.T, key string, models ...string) *mock.Provider {
	t.Helper()
	ip := lanAddr(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skipf("cannot bind %s: %v", ip, err)
	}
	p := mock.New(key, models...)
	p.Server.Close()
	p.Server = httptest.NewUnstartedServer(p.Server.Config.Handler)
	p.Server.Listener.Close()
	p.Server.Listener = ln
	p.Server.Start()
	return p
}

// Checkpoint 4 (§4.10.4): replaying 100 synthetic requests across 2 apps and 3 models
// (mixed local/cloud) reproduces exact per-app and per-model counts, and the egress view
// classifies local vs off-machine with zero misattribution, against golden totals.
func TestCheckpointObservabilityAndEgress(t *testing.T) {
	r := newRig(t)

	localUp := mock.New("", "qwen3:8b", "nomic-embed-text")
	defer localUp.Close()
	cloudUp := offMachineMock(t, "sk-cloud", "claude-sonnet-5")
	defer cloudUp.Close()

	r.addLocal(t, "ollama", localUp)
	r.addSource(t, "anthropic", "anthropic", "sk-cloud", cloudUp)

	tokA := r.mint(t, "editor")
	tokB := r.mint(t, "agent")

	// The golden plan: exactly what we will send, and therefore exactly what the ledger
	// must say afterwards. Every number below is derived from this table, never from the
	// ledger it is checking.
	type call struct {
		token, app, model, egress string
		n                         int
	}
	plan := []call{
		{tokA, "editor", "qwen3:8b", store.EgressLocal, 40},
		{tokA, "editor", "claude-sonnet-5", store.EgressCloud, 10},
		{tokB, "agent", "qwen3:8b", store.EgressLocal, 25},
		{tokB, "agent", "nomic-embed-text", store.EgressLocal, 15},
		{tokB, "agent", "claude-sonnet-5", store.EgressCloud, 10},
	}
	goldenByApp := map[string]int{}
	goldenByModel := map[string]int{}
	goldenByEgress := map[string]int{}
	total := 0
	for _, c := range plan {
		goldenByApp[c.app] += c.n
		goldenByModel[c.model] += c.n
		goldenByEgress[c.egress] += c.n
		total += c.n
	}
	if total != 100 {
		t.Fatalf("the plan replays %d requests, want 100", total)
	}

	for _, c := range plan {
		for i := 0; i < c.n; i++ {
			resp, raw := r.chat(t, c.token, c.model, false)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s/%s: HTTP %d: %s", c.app, c.model, resp.StatusCode, raw)
			}
		}
	}

	assertCounts := func(by string, golden map[string]int, pick func(store.Bucket) string) {
		t.Helper()
		buckets, err := r.app.DB.Aggregate([]string{by}, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		sum := 0
		for _, b := range buckets {
			got[pick(b)] = b.Requests
			sum += b.Requests
		}
		if sum != total {
			t.Errorf("by %s: %d requests recorded, want %d", by, sum, total)
		}
		for k, want := range golden {
			if got[k] != want {
				t.Errorf("by %s: %q recorded %d, want %d", by, k, got[k], want)
			}
		}
		for k, n := range got {
			if _, ok := golden[k]; !ok {
				t.Errorf("by %s: unexpected group %q with %d requests", by, k, n)
			}
		}
	}
	assertCounts("app", goldenByApp, func(b store.Bucket) string { return b.App })
	assertCounts("model", goldenByModel, func(b store.Bucket) string { return b.ModelID })
	assertCounts("egress", goldenByEgress, func(b store.Bucket) string { return b.Egress })

	// And the egress view itself, through the control op the panel reads.
	view := r.opJSON(t, "egress_summary", map[string]any{"since_hours": float64(0)})
	local := view["local"].(map[string]any)
	cloud := view["cloud"].(map[string]any)
	if int(local["requests"].(float64)) != goldenByEgress[store.EgressLocal] {
		t.Errorf("egress view: local %v, want %d", local["requests"], goldenByEgress[store.EgressLocal])
	}
	if int(cloud["requests"].(float64)) != goldenByEgress[store.EgressCloud] {
		t.Errorf("egress view: off-machine %v, want %d", cloud["requests"], goldenByEgress[store.EgressCloud])
	}
	if got := view["requests"].(float64); int(got) != total {
		t.Errorf("egress view: %v requests, want %d", got, total)
	}

	// Per-app spend must attribute cost only where a priced model was actually used.
	usage := r.opJSON(t, "usage_summary", map[string]any{"by": []any{"app"}, "since_hours": float64(0)})
	buckets := usage["buckets"].([]any)
	if len(buckets) != 2 {
		t.Fatalf("usage grouped into %d apps, want 2", len(buckets))
	}
	for _, b := range buckets {
		m := b.(map[string]any)
		if m["cost"].(float64) <= 0 {
			t.Errorf("app %v priced at 0 despite 10 cloud calls", m["app"])
		}
	}
}

// The egress classifier decides what left the machine. It is the one number in this
// product that must never be wrong, so it is checked against a table, not by inference
// from a live call.
func TestEgressClassifierGoldenTable(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		{"http://127.0.0.1:11434/v1", store.EgressLocal},
		{"http://localhost:1234/v1", store.EgressLocal},
		{"http://[::1]:8080/v1", store.EgressLocal},
		{"http://127.0.0.2:8080/v1", store.EgressLocal},
		{"http://app.localhost:3000/v1", store.EgressLocal},
		{"https://api.anthropic.com/v1", store.EgressCloud},
		{"https://api.deepseek.com/v1", store.EgressCloud},
		{"http://192.168.1.40:11434/v1", store.EgressCloud},
		{"http://10.0.0.5:8080/v1", store.EgressCloud},
		{"http://not-a-real-host.invalid/v1", store.EgressCloud},
		{"", store.EgressCloud},
		{"::::", store.EgressCloud},
	}
	for _, c := range cases {
		if got := router.Egress(c.url); got != c.want {
			t.Errorf("Egress(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// Content logging is off by default (§4.5), and the ledger holds no prompt text.
func TestLedgerHoldsNoContentByDefault(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "privacy")

	const canary = "the-prompt-nobody-should-store"
	body := fmt.Sprintf(`{"model":"qwen3:8b","messages":[{"role":"user","content":%q}]}`, canary)
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := r.app.DB.Setting(store.SetContentLogging, "off"); got != "off" {
		t.Fatalf("content logging defaults to %q, want off", got)
	}
	rows, err := r.app.DB.SQL().Query(`SELECT * FROM ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			if s, ok := v.(string); ok && contains(s, canary) {
				t.Fatalf("ledger column %s holds prompt content", cols[i])
			}
		}
	}
	_ = context.Background()
}

func contains(hay, needle string) bool {
	return len(needle) > 0 && len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// If Ferrule cannot record a request, it does not make it.
//
// The egress view is the product — "you see, on your own disk, exactly what left your
// machine". A request routed with no ledger row is precisely the thing that claim
// forbids, so an unwritable ledger is a refusal rather than a silent send. The ledger is
// genuinely broken here, not mocked.
func TestARequestFerruleCannotRecordIsNotMade(t *testing.T) {
	r := newRig(t)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "accountable-app")

	before := len(up.Requests())
	if _, err := r.app.DB.SQL().Exec(`DROP TABLE ledger`); err != nil {
		t.Fatal(err)
	}

	resp, raw := r.chat(t, tok, "qwen3:8b", false)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a request was routed with no ledger row: %s", raw)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("HTTP %d, want 503", resp.StatusCode)
	}
	if after := up.Requests()[before:]; len(after) != 0 {
		t.Fatalf("the provider was contacted for a request that could not be recorded: %+v", after)
	}
}

// A row is written before the request leaves, so a daemon that dies mid-request leaves
// evidence that traffic happened rather than no trace at all.
func TestTheLedgerRowExistsBeforeTheRequestLeaves(t *testing.T) {
	r := newRig(t)

	seen := make(chan int, 1)
	up := mock.New("", "qwen3:8b")
	defer up.Close()
	r.addLocal(t, "ollama", up)
	tok := r.mint(t, "midflight")

	// Count the rows the moment the upstream is being called — i.e. after Ferrule has
	// decided to send and before it has seen a response.
	up.BeforeRespond = func() {
		var n int
		_ = r.app.DB.SQL().QueryRow(`SELECT COUNT(*) FROM ledger WHERE status = ?`,
			store.StatusInFlight).Scan(&n)
		select {
		case seen <- n:
		default:
		}
	}

	resp, _ := r.chat(t, tok, "qwen3:8b", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	select {
	case n := <-seen:
		if n < 1 {
			t.Error("no in-flight row existed while the request was in the air")
		}
	default:
		t.Fatal("the upstream was never reached")
	}
	// And it is completed afterwards, not left in-flight.
	var stuck int
	if err := r.app.DB.SQL().QueryRow(`SELECT COUNT(*) FROM ledger WHERE status = ?`,
		store.StatusInFlight).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck != 0 {
		t.Errorf("%d row(s) left in-flight after a completed request", stuck)
	}
}
