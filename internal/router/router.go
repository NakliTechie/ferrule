// Package router is Ferrule's raw-tokens lane: one OpenAI-compatible surface in front of
// every chat, completion, and embeddings model the vault can reach (§2.5, §4.3.1).
//
// Auth on this surface is a Ferrule app token, never a provider key. The provider key is
// fetched from the vault at call time, attached to the upstream request, and dropped.
package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"ferrule/internal/catalog"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
	"ferrule/internal/vault"
)

// Router serves the OpenAI-compatible endpoints.
type Router struct {
	db     *store.DB
	vault  vault.Vault
	cat    *catalog.Catalog
	client *http.Client
}

// New builds a Router.
func New(db *store.DB, v vault.Vault, cat *catalog.Catalog) *Router {
	return &Router{
		db: db, vault: v, cat: cat,
		// No overall timeout: a long generation is a legitimate request. Per-attempt
		// deadlines come from the caller's context.
		client: &http.Client{Timeout: 0},
	}
}

// Mount registers the raw-tokens lane on mux.
func (r *Router) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/v1/chat/completions", r.guard(func(w http.ResponseWriter, req *http.Request, g store.Grant) {
		r.forward(w, req, g, "chat/completions")
	}))
	mux.HandleFunc("/v1/completions", r.guard(func(w http.ResponseWriter, req *http.Request, g store.Grant) {
		r.forward(w, req, g, "completions")
	}))
	mux.HandleFunc("/v1/embeddings", r.guard(func(w http.ResponseWriter, req *http.Request, g store.Grant) {
		r.forward(w, req, g, "embeddings")
	}))
	mux.HandleFunc("/v1/images/generations", r.guard(func(w http.ResponseWriter, req *http.Request, g store.Grant) {
		r.forward(w, req, g, "images/generations")
	}))
	mux.HandleFunc("/v1/models", r.guard(r.listModels))
}

// guard authenticates an app token and hands the grant to the handler.
func (r *Router) guard(h func(http.ResponseWriter, *http.Request, store.Grant)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tok := bearer(req)
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, i18n.T("grant.missing"))
			return
		}
		g, err := r.db.GrantByToken(tok)
		if err != nil || g.Revoked() {
			writeErr(w, http.StatusUnauthorized, i18n.T("grant.rejected"))
			return
		}
		h(w, req, g)
	}
}

func bearer(req *http.Request) string {
	h := req.Header.Get("Authorization")
	if h == "" {
		return req.Header.Get("X-Api-Key")
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

// listModels answers Ferrule's own /v1/models: every model on a live source, plus every
// alias, so a client's model picker shows what the router can actually serve.
func (r *Router) listModels(w http.ResponseWriter, req *http.Request, _ store.Grant) {
	srcs, err := r.db.Sources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	live := map[string]store.Source{}
	for _, s := range srcs {
		if s.Status == store.StatusLive {
			live[s.ID] = s
		}
	}
	models, err := r.db.Models("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	data := []any{}
	for _, m := range models {
		s, ok := live[m.SourceID]
		if !ok || s.Lane != store.LaneTokens {
			continue
		}
		data = append(data, map[string]any{
			"id": m.ModelID, "object": "model", "owned_by": s.Name,
			"ferrule": map[string]any{
				"source": s.Name, "where": s.Kind, "capabilities": m.Capabilities,
				"context_length": m.ContextLength,
			},
		})
	}
	aliases, _ := r.db.Aliases()
	for _, a := range aliases {
		data = append(data, map[string]any{
			"id": a.Name, "object": "model", "owned_by": "ferrule",
			"ferrule": map[string]any{"alias": true, "rungs": len(a.Rungs)},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// forward runs the request down the ladder: try each rung, fall through on an upstream
// failure, and record a ledger row for every attempt that actually left.
func (r *Router) forward(w http.ResponseWriter, req *http.Request, g store.Grant, path string) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeErr(w, http.StatusBadRequest, "request body is not JSON: "+err.Error())
		return
	}
	requested, _ := envelope["model"].(string)
	targets, err := r.Resolve(requested)
	if err != nil {
		_ = r.db.Record(store.Entry{
			GrantID: g.ID, App: g.App, RequestedModel: requested,
			Status: http.StatusNotFound, Err: err.Error(), ReqBytes: len(body),
		})
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	stream, _ := envelope["stream"].(bool)
	var lastErr string
	for i, t := range targets {
		if t.Source.Lane != store.LaneTokens {
			lastErr = i18n.T("route.lanePassthrough", requested, t.Source.Name)
			continue
		}
		outcome := r.attempt(w, req.Context(), g, t, path, envelope, body, stream, requested)
		if outcome.served {
			return
		}
		lastErr = outcome.err
		if !outcome.retryable || i == len(targets)-1 {
			// A client error is the client's to fix; trying it on another model would
			// turn a clear 400 into a confusing cascade.
			writeErr(w, outcome.status, outcome.err)
			return
		}
	}
	writeErr(w, http.StatusBadGateway, lastErr)
}

type outcome struct {
	served    bool
	retryable bool
	status    int
	err       string
}

func (r *Router) attempt(w http.ResponseWriter, ctx context.Context, g store.Grant, t Target,
	path string, envelope map[string]any, body []byte, stream bool, requested string) outcome {

	spec, ok := provider.Get(t.Source.Provider)
	if !ok {
		return outcome{retryable: true, status: http.StatusBadGateway,
			err: i18n.T("source.unknownProvider", t.Source.Provider, provider.Names())}
	}
	key := ""
	if t.Source.KeyRef != "" {
		var err error
		if key, err = r.vault.Get(t.Source.KeyRef); err != nil {
			return outcome{retryable: true, status: http.StatusBadGateway, err: err.Error()}
		}
	}

	// Rewrite the model field to the real upstream id. Everything else the client sent
	// passes through untouched.
	upstreamBody := body
	if t.Model.ModelID != requested {
		clone := make(map[string]any, len(envelope))
		for k, v := range envelope {
			clone[k] = v
		}
		clone["model"] = t.Model.ModelID
		if b, err := json.Marshal(clone); err == nil {
			upstreamBody = b
		}
	}

	egress := Egress(t.Source.BaseURL)
	entry := store.Entry{
		GrantID: g.ID, App: g.App, SourceID: t.Source.ID, Provider: t.Source.Provider,
		ModelID: t.Model.ModelID, RequestedModel: requested, Lane: store.LaneTokens,
		Egress: egress, ReqBytes: len(upstreamBody),
	}

	start := time.Now()
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		provider.URL(t.Source.BaseURL, path), bytes.NewReader(upstreamBody))
	if err != nil {
		return outcome{retryable: false, status: http.StatusInternalServerError, err: err.Error()}
	}
	upReq.Header.Set("Content-Type", "application/json")
	if stream {
		upReq.Header.Set("Accept", "text/event-stream")
	}
	spec.Authorize(upReq, key)

	resp, err := r.client.Do(upReq)
	if err != nil {
		entry.LatencyMS = int(time.Since(start).Milliseconds())
		entry.Status, entry.Err = http.StatusBadGateway, i18n.T("route.upstreamFailed", t.Source.Name, err.Error())
		_ = r.db.Record(entry)
		return outcome{retryable: true, status: http.StatusBadGateway, err: entry.Err}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		entry.LatencyMS = int(time.Since(start).Milliseconds())
		entry.Status, entry.RespBytes = resp.StatusCode, len(raw)
		entry.Err = i18n.T("route.upstreamFailed", t.Source.Name, strings.TrimSpace(string(raw)))
		_ = r.db.Record(entry)
		// 5xx and 429 are the upstream's problem; the next rung may well answer.
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return outcome{retryable: retry, status: resp.StatusCode, err: entry.Err}
	}

	// From here the response is being served; a later failure is a truncated stream, not
	// a reason to try another rung.
	copyHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	var usage usageCounts
	var n int
	var served []byte
	logContent := r.db.ContentLoggingOn()
	if stream {
		n, usage, served = pipeStream(w, resp.Body, logContent)
	} else {
		raw, _ := io.ReadAll(resp.Body)
		n = len(raw)
		usage = parseUsage(raw)
		_, _ = w.Write(raw)
		if logContent {
			served = raw
		}
	}

	entry.LatencyMS = int(time.Since(start).Milliseconds())
	entry.Status, entry.RespBytes = resp.StatusCode, n
	entry.PromptTokens, entry.CompletionTokens = usage.Prompt, usage.Completion
	entry.Cost = Cost(t.Model, usage.Prompt, usage.Completion)
	_ = r.db.Record(entry)
	if logContent {
		// Off by default, local only, and stored apart from the ledger (§4.5).
		id, _ := r.db.LastLedgerID()
		_ = r.db.RecordContent(store.Content{
			LedgerID: id, App: g.App, Model: t.Model.ModelID,
			Request: string(upstreamBody), Response: string(served),
		})
	}
	return outcome{served: true}
}

// Cost prices a call from the model's catalog-sourced per-million-token rates.
func Cost(m store.Model, prompt, completion int) float64 {
	return float64(prompt)/1e6*m.InCost + float64(completion)/1e6*m.OutCost
}

type usageCounts struct{ Prompt, Completion int }

func parseUsage(raw []byte) usageCounts {
	var doc struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return usageCounts{}
	}
	return usageCounts{doc.Usage.PromptTokens, doc.Usage.CompletionTokens}
}

// pipeStream relays SSE to the client as it arrives and picks the usage block out of the
// stream on the way past. Relaying is never delayed to read the counts.
func pipeStream(w http.ResponseWriter, body io.Reader, keep bool) (int, usageCounts, []byte) {
	fl, _ := w.(http.Flusher)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	total := 0
	var usage usageCounts
	var kept []byte
	for sc.Scan() {
		line := sc.Bytes()
		out := append(append([]byte(nil), line...), '\n')
		n, _ := w.Write(out)
		total += n
		if keep && len(kept) < 1<<20 {
			kept = append(kept, out...)
		}
		if fl != nil {
			fl.Flush()
		}
		if payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data: ")); ok {
			if bytes.Contains(payload, []byte(`"usage"`)) {
				if u := parseUsage(payload); u.Prompt > 0 || u.Completion > 0 {
					usage = u
				}
			}
		}
	}
	return total, usage, kept
}

func copyHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"Content-Type", "Cache-Control", "Connection"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	if code == 0 {
		code = http.StatusBadGateway
	}
	writeJSON(w, code, map[string]any{"error": map[string]any{
		"message": msg, "type": "ferrule_error", "code": code,
	}})
}
