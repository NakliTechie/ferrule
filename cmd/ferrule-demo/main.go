// Command ferrule-demo stands up a whole Ferrule with fake providers, so the product can
// be evaluated end to end without owning a single API key.
//
// It is a development tool, not part of the product: `make dist` does not build it, and
// nothing in `cmd/ferrule` imports it. Everything it creates lives in a scratch config
// directory it prints, and it stops when you do.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NakliTechie/ferrule/internal/api"
	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/discovery"
	"github.com/NakliTechie/ferrule/internal/mock"
	"github.com/NakliTechie/ferrule/internal/server"
)

func main() {
	dir := flag.String("dir", "", "config directory (default: a fresh temp dir)")
	port := flag.Int("port", 8877, "port for the demo daemon")
	traffic := flag.Int("traffic", 120, "synthetic requests to replay, so Usage has something to show")
	clean := flag.Bool("clean", false, "omit the deliberately-failing source (for screenshots)")
	flag.Parse()

	if err := run(*dir, *port, *traffic, *clean); err != nil {
		fmt.Fprintln(os.Stderr, "ferrule-demo:", err)
		os.Exit(1)
	}
}

func run(dir string, port, traffic int, clean bool) error {
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "ferrule-demo-"); err != nil {
			return err
		}
	}
	fmt.Println("config:", dir)

	// Fake providers. The "cloud" ones bind to a non-loopback address on this machine so
	// the egress view has something honest to call off-machine — no packet leaves the
	// host, but it does leave the loopback interface, which is what the classifier reads.
	local := mock.New("", "qwen3:8b", "llama-3.1-8b-instruct", "nomic-embed-text", "llava:13b", "gemma3:12b")
	defer local.Close()

	cloud := map[string]*mock.Provider{
		"anthropic": offMachine("sk-ant-demo-not-a-real-key", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"),
		"deepseek":  offMachine("sk-demo-not-a-real-key", "deepseek-chat", "deepseek-reasoner"),
		"groq":      offMachine("gsk_demo-not-a-real-key", "llama-3.3-70b-versatile", "llama-4-scout-17b", "mixtral-8x7b-32768"),
	}
	keys := map[string]string{"anthropic": "sk-ant-demo-not-a-real-key", "deepseek": "sk-demo-not-a-real-key", "groq": "gsk_demo-not-a-real-key"}
	for _, p := range cloud {
		if p != nil {
			defer p.Close()
		}
	}

	a, err := app.Open(app.Options{Dir: dir})
	if err != nil {
		return err
	}
	defer a.Close()
	ctx := context.Background()
	bus := api.NewBus(a)

	// A detected local runtime, adopted with no input — the same path a real Ollama takes.
	a.Discovery.SetDetectURLs("ollama", []string{local.BaseURL()})
	a.Discovery.SetDetectURLs("lmstudio", nil)
	a.Discovery.SetDetectURLs("llamacpp", nil)
	if _, err := a.Discovery.Detect(ctx); err != nil {
		return err
	}

	for name, p := range cloud {
		if p == nil {
			continue
		}
		if _, err := a.Discovery.Add(ctx, discovery.AddRequest{
			Name: name, Provider: name, BaseURL: p.BaseURL(), Key: keys[name],
			// The fixtures are http on this host's LAN interface, so the egress view has
			// something honest to call off-machine. Ferrule refuses that unless it is
			// acknowledged, so the demo acknowledges it.
			AllowInsecure: true,
		}); err != nil {
			return err
		}
	}
	// One source that cannot work, so the loud-failure path is on screen too. Omitted
	// with -clean, which is what a hero screenshot wants.
	if !clean {
		if _, err := a.Discovery.Add(ctx, discovery.AddRequest{
			Name: "groq-typo", Provider: "groq", BaseURL: "http://127.0.0.1:1/v1", Key: "gsk_wrong-not-a-real-key",
		}); err != nil {
			return err
		}
	}

	src := map[string]string{}
	sources, err := a.DB.Sources()
	if err != nil {
		return err
	}
	for _, s := range sources {
		src[s.Name] = s.ID
	}
	// Ladders are built only from sources that actually exist. A host with no
	// non-loopback interface has no cloud fakes, and naming one anyway produced an alias
	// that could never serve.
	rung := func(source, model string) []any {
		if id, ok := src[source]; ok {
			return []any{id + "/" + model}
		}
		return nil
	}
	for _, al := range []struct {
		name  string
		rungs [][]any
	}{
		{"fast", [][]any{rung("ollama", "qwen3:8b"), rung("groq", "llama-3.3-70b-versatile")}},
		{"smart", [][]any{rung("anthropic", "claude-opus-5"), rung("deepseek", "deepseek-reasoner")}},
		{"cheap", [][]any{rung("ollama", "gemma3:12b"), rung("deepseek", "deepseek-chat")}},
		{"vision", [][]any{rung("ollama", "llava:13b"), rung("anthropic", "claude-sonnet-5")}},
	} {
		var ladder []any
		for _, r := range al.rungs {
			ladder = append(ladder, r...)
		}
		if len(ladder) == 0 {
			continue
		}
		if _, err := bus.Dispatch(ctx, "set_alias",
			api.Args{"name": al.name, "ladder": ladder}, api.DoorCLI, "demo"); err != nil {
			return err
		}
	}
	if _, err := bus.Dispatch(ctx, "set_remap",
		api.Args{"from": "gpt-4o", "to": "smart"}, api.DoorCLI, "demo"); err != nil {
		return err
	}

	// Bound the way the product binds, so what the demo shows is what an install
	// looks like — including the address and household key a person hands out. A
	// loopback-only demo shows the one state a real install is almost never in.
	srv, err := server.New(a, server.Options{
		Addr: fmt.Sprintf("0.0.0.0:%d", port),
		// A documentation address (RFC 5737), so a screenshot of the demo does not
		// publish whoever took it.
		Advertise: fmt.Sprintf("192.0.2.42:%d", port),
	})
	if err != nil {
		return err
	}
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Serve(runCtx)
	base := "http://" + srv.Addr()

	tokens := map[string]string{}
	for _, appName := range []string{"zed", "transcribe.sh", "research-agent"} {
		res, err := bus.Dispatch(ctx, "mint_grant", api.Args{"app": appName}, api.DoorCLI, "demo")
		if err != nil {
			return err
		}
		tokens[appName] = res.(map[string]any)["token"].(string)
	}

	if traffic > 0 {
		fmt.Printf("replaying %d requests so Usage and Egress have something to show…\n", traffic)
		ok, failed := replay(base, tokens, traffic)
		if failed > 0 {
			// Say so rather than letting a short run look like a full one.
			fmt.Printf("  %d succeeded, %d failed\n", ok, failed)
		}
	}

	fmt.Println()
	fmt.Println("  Ferrule demo:", base)
	fmt.Println()
	fmt.Println("  Every provider above is a local fake. No key you own is involved, and")
	fmt.Println("  nothing leaves this machine.")
	fmt.Println()
	fmt.Println("  Point any OpenAI-compatible client at it:")
	fmt.Printf("    OPENAI_BASE_URL=%s/v1\n", base)
	fmt.Printf("    OPENAI_API_KEY=%s\n", tokens["zed"])
	fmt.Println()
	fmt.Printf("  Or drive the CLI against the same state:\n")
	fmt.Printf("    FERRULE_CONFIG_DIR=%s ./ferrule status\n", dir)
	fmt.Println()
	fmt.Println("  Ctrl-C to stop. The config directory above is yours to delete.")

	<-runCtx.Done()
	fmt.Println("\nstopped")
	return nil
}

// offMachine binds a mock to a non-loopback interface on this host, so the egress
// classifier reads it as off-machine. Returns nil when the host has no such address,
// in which case the demo simply has fewer sources.
func offMachine(key string, models ...string) *mock.Provider {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ipnet.IP.String(), "0"))
		if err != nil {
			continue
		}
		p := mock.New(key, models...)
		p.Server.Close()
		p.Server = httptest.NewUnstartedServer(p.Server.Config.Handler)
		p.Server.Listener.Close()
		p.Server.Listener = ln
		p.Server.Start()
		return p
	}
	return nil
}

// replay drives real requests through the real router, so the ledger it fills is the
// ledger the product writes — not fixture rows inserted behind its back.
func replay(base string, tokens map[string]string, n int) (ok, failed int) {
	type call struct{ app, model string }
	plan := []call{
		{"zed", "fast"}, {"zed", "fast"}, {"zed", "smart"},
		{"research-agent", "cheap"}, {"research-agent", "gpt-4o"},
		{"transcribe.sh", "nomic-embed-text"},
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for i := 0; i < n; i++ {
		c := plan[rand.Intn(len(plan))]
		body, _ := json.Marshal(map[string]any{
			"model":    c.model,
			"messages": []map[string]string{{"role": "user", "content": "demo"}},
		})
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
			strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+tokens[c.app])
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			failed++
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			failed++
			continue
		}
		ok++
	}
	return ok, failed
}
