// Package mock is a stand-in provider used by the checkpoint harnesses. It speaks the
// OpenAI dialect for listing, chat, embeddings, and streaming, plus a Replicate-shaped
// prediction surface — enough to exercise both lanes without a network or a real key.
package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// PredictionFixture is the exact response the mock returns for a prediction, byte for
// byte, so a test can compare against it literally.
const PredictionFixture = `{"id":"pred_harness_1","status":"succeeded","model":"black-forest-labs/flux-schnell","output":["https://example.invalid/out.webp"],"metrics":{"predict_time":1.25}}`

// Refusal is an upstream saying no, in the shape a real provider says it.
type Refusal struct {
	Status int
	Body   string
}

// Provider is a fake upstream.
type Provider struct {
	Server *httptest.Server
	// Key, when non-empty, must be presented as a Bearer token.
	Key string
	// Models are the ids the listing returns.
	Models []string
	// Fail makes every request answer 500, to exercise ladder fallback.
	Fail bool
	// ChunkDelay spaces out streamed chunks, so a caller can prove it is receiving them
	// as they are produced rather than in one buffered lump at the end.
	ChunkDelay time.Duration
	// BeforeRespond runs once the mock has the request and before it answers, so a test
	// can observe Ferrule's state at the moment a request is in the air.
	BeforeRespond func()
	// RefuseChat, when set, answers inference for that model with this status and body.
	// Set per model id, or under "" for every model. This is how the real refusals —
	// DeepSeek's 402 and NVIDIA's per-model 404 — are replayed as fixtures.
	//
	// It covers embeddings as well as chat: a provider refusing a model refuses it, and
	// a fixture that still served embeddings let a source pass a test its real
	// counterpart would have failed.
	RefuseChat map[string]Refusal
	// Down makes the server refuse connections after Stop.
	mu       sync.Mutex
	requests []Request
}

// Request is one recorded upstream call.
type Request struct {
	Method string
	Path   string
	Auth   string
	Body   string
	Header http.Header
}

// New starts a mock provider. Its base URL (with /v1) is Provider.BaseURL().
func New(key string, models ...string) *Provider {
	p := &Provider{Key: key, Models: models}
	p.Server = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

// BaseURL is the OpenAI-style base, ready to hand to a source.
func (p *Provider) BaseURL() string { return p.Server.URL + "/v1" }

// Close shuts the mock down.
func (p *Provider) Close() { p.Server.Close() }

// Requests returns everything the mock was asked, in order.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

func (p *Provider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.requests = append(p.requests, Request{
		Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		Body: string(body), Header: r.Header.Clone(),
	})
	p.mu.Unlock()

	if p.BeforeRespond != nil {
		p.BeforeRespond()
	}
	if p.Key != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got != p.Key {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
	}
	if p.Fail {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"upstream is having a day"}}`)
		return
	}

	switch {
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
		p.writeModels(w)
	case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		if ref, ok := p.refusalFor(req.Model); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ref.Status)
			_, _ = w.Write([]byte(ref.Body))
			return
		}
		p.writeChat(w, body)
	case r.URL.Path == "/v1/embeddings" && r.Method == http.MethodPost:
		var ereq struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &ereq)
		if ref, ok := p.refusalFor(ereq.Model); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ref.Status)
			_, _ = w.Write([]byte(ref.Body))
			return
		}
		writeJSON(w, map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
			"usage":  map[string]any{"prompt_tokens": 2, "total_tokens": 2},
		})
	case r.URL.Path == "/v1/account" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{"type": "user", "username": "harness"})
	case strings.HasPrefix(r.URL.Path, "/v1/collections/"):
		writeJSON(w, map[string]any{"models": []any{
			map[string]any{"owner": "black-forest-labs", "name": "flux-schnell", "description": "fast image"},
		}})
	case r.URL.Path == "/v1/predictions" && r.Method == http.MethodPost:
		// Written as literal bytes, not marshalled from a map. The passthrough lane
		// promises the response reaches the caller unaltered, and a fixture that is
		// itself re-serialised cannot tell you whether that promise held: Go sorts map
		// keys, so a byte comparison would be measuring the fixture, not the proxy.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(PredictionFixture))
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"no route %s"}}`, r.URL.Path)
	}
}

// refusalFor returns the configured refusal for a model, falling back to the wildcard.
func (p *Provider) refusalFor(model string) (Refusal, bool) {
	if p.RefuseChat == nil {
		return Refusal{}, false
	}
	if ref, ok := p.RefuseChat[model]; ok {
		return ref, true
	}
	ref, ok := p.RefuseChat[""]
	return ref, ok
}

func (p *Provider) writeModels(w http.ResponseWriter) {
	data := make([]any, 0, len(p.Models))
	for _, m := range p.Models {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "mock"})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func (p *Provider) writeChat(w http.ResponseWriter, body []byte) {
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, chunk := range []string{"ferrule", " routed", " this"} {
			// A real provider does not emit a whole completion at once. Spacing the
			// chunks is what lets a test tell a relay from a buffer.
			if p.ChunkDelay > 0 {
				time.Sleep(p.ChunkDelay)
			}
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
				"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": req.Model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": chunk}}},
			}))
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
		}))
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	writeJSON(w, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion", "model": req.Model,
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": "ferrule routed this"},
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(mustJSON(v)))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"marshal"}`
	}
	return string(b)
}
