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
)

// Provider is a fake upstream.
type Provider struct {
	Server *httptest.Server
	// Key, when non-empty, must be presented as a Bearer token.
	Key string
	// Models are the ids the listing returns.
	Models []string
	// Fail makes every request answer 500, to exercise ladder fallback.
	Fail bool
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
		p.writeChat(w, body)
	case r.URL.Path == "/v1/embeddings" && r.Method == http.MethodPost:
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
		writeJSON(w, map[string]any{
			"id": "pred_harness_1", "status": "succeeded", "model": "black-forest-labs/flux-schnell",
			"output":  []string{"https://example.invalid/out.webp"},
			"metrics": map[string]any{"predict_time": 1.25},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"no route %s"}}`, r.URL.Path)
	}
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
