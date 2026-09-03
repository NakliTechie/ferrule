package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"ferrule/internal/provider"
	"ferrule/internal/store"
)

// trimKnown removes an exact secret before the shape net runs.
func trimKnown(s, secret string) string {
	if secret != "" && len(secret) >= 8 {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return trim(s)
}

// httpDo issues a request against a source with its credentials attached.
func (e *Engine) httpDo(ctx context.Context, spec provider.Spec, method, url, key string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	spec.Authorize(req, key)
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, raw, err
}

// listedModel is a model id as the source reported it, before classification.
type listedModel struct {
	ID      string
	Display string
	Context int
}

// list runs the provider's model-listing dialect. This is the probe step (§2.3).
// A failure comes back as a typed Reason, never as prose.
func (e *Engine) list(ctx context.Context, spec provider.Spec, baseURL, key string) ([]listedModel, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	switch spec.Listing {
	case provider.ListReplicate:
		return e.listReplicate(ctx, spec, baseURL, key)
	default:
		return e.listOpenAI(ctx, spec, baseURL, key)
	}
}

func (e *Engine) listOpenAI(ctx context.Context, spec provider.Spec, baseURL, key string) ([]listedModel, error) {
	url := provider.URL(baseURL, "models")
	code, raw, err := e.httpDo(ctx, spec, http.MethodGet, url, key, nil)
	if err != nil {
		return nil, newReason(CodeUnreachable, url, trim(err.Error()))
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return nil, newReason(CodeBadKey, code)
	}
	if code >= 300 && code < 400 {
		return nil, newReason(CodeRedirect, trimKnown(string(raw), key))
	}
	if code != http.StatusOK {
		return nil, newReason(CodeBadStatus, code, trimKnown(string(raw), key))
	}
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			Object        string `json:"object"`
			DisplayName   string `json:"display_name"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxContext    int    `json:"max_context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, newReason(CodeNoModels)
	}
	out := make([]listedModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" {
			continue
		}
		disp := m.DisplayName
		if disp == "" {
			disp = m.Name
		}
		ctxLen := m.ContextLength
		if ctxLen == 0 {
			ctxLen = m.MaxContext
		}
		out = append(out, listedModel{ID: m.ID, Display: disp, Context: ctxLen})
	}
	if len(out) == 0 {
		return nil, newReason(CodeNoModels)
	}
	return out, nil
}

func (e *Engine) listReplicate(ctx context.Context, spec provider.Spec, baseURL, key string) ([]listedModel, error) {
	// The account endpoint is the honest key test: it 401s on a bad token and costs nothing.
	code, raw, err := e.httpDo(ctx, spec, http.MethodGet, provider.URL(baseURL, "account"), key, nil)
	if err != nil {
		return nil, newReason(CodeUnreachable, baseURL, trim(err.Error()))
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return nil, newReason(CodeBadKey, code)
	}
	if code != http.StatusOK {
		return nil, newReason(CodeBadStatus, code, trimKnown(string(raw), key))
	}
	code, raw, err = e.httpDo(ctx, spec, http.MethodGet, provider.URL(baseURL, "collections/text-to-image"), key, nil)
	if err != nil || code != http.StatusOK {
		// The key works but the catalogue does not. A live source with nothing on it is
		// a source that cannot be routed to and that no interface can explain — so it
		// fails, with the reason, rather than sitting green and empty.
		return nil, newReason(CodeNoModels)
	}
	var doc struct {
		Models []struct {
			Owner       string `json:"owner"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, newReason(CodeNoModels)
	}
	out := make([]listedModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.Owner == "" || m.Name == "" {
			continue
		}
		out = append(out, listedModel{ID: m.Owner + "/" + m.Name, Display: m.Description})
	}
	if len(out) == 0 {
		return nil, newReason(CodeNoModels)
	}
	return out, nil
}

// listBudget covers a model listing: an HTTP round trip, plus whatever a cold runtime
// spends scanning its own model directory.
const listBudget = 30 * time.Second

// probeBudget is how long one test or classification request may take.
//
// A cloud provider that has not answered in 45 seconds is not going to. A local runtime
// is a different animal: its first chat completion has to load the model into memory,
// which on a laptop with an 8B model is minutes, not seconds. Judging a local runtime by
// a cloud provider's clock marks a working runtime dead — which is exactly what a short
// budget did here before this comment existed.
func probeBudget(spec provider.Spec) time.Duration {
	if spec.Kind == store.KindLocal {
		return 4 * time.Minute
	}
	return 45 * time.Second
}

// probeChat fires one minimal chat request. Used as the live-probe classifier and as the
// test step for the raw-tokens lane. The second return distinguishes a timeout from a
// refusal: they call for different next moves.
func (e *Engine) probeChat(ctx context.Context, spec provider.Spec, baseURL, key, modelID string) (bool, Reason) {
	body, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
		"stream":     false,
	})
	budget := probeBudget(spec)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	code, raw, err := e.httpDo(ctx, spec, http.MethodPost, provider.URL(baseURL, "chat/completions"), key, body)
	if err != nil {
		return false, timeoutOrFailure(ctx, err, budget)
	}
	if code == http.StatusOK {
		return true, Reason{Code: CodeOK}
	}
	return false, classifyRefusal(code, trimKnown(string(raw), key))
}

// classifyRefusal names what the provider actually refused.
//
// A single "test_failed" collapsed three different situations with three different next
// moves: the key is wrong, the account has no money, and this particular model is not
// yours. The first two were verified against real providers — DeepSeek answers 402 with
// "Insufficient Balance" and NVIDIA answers 404 for a model outside your tier — and in
// both cases the key had already proven itself by listing the account's models. Telling
// someone to check their key when their key is fine sends them to fix the wrong thing.
func classifyRefusal(code int, body string) Reason {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return newReason(CodeBadKey, code)
	case code == http.StatusPaymentRequired:
		return newReason(CodeNoBalance, code, body)
	case code == http.StatusNotFound || code == http.StatusGone:
		// Model-level, and the caller decides whether to try another one. NVIDIA answers
		// 404 for a model outside your tier and 410 for one it has retired; both are
		// facts about that model id and neither says anything about the account.
		return Reason{Code: CodeModelUnavailable, Message: CodeBadStatus.message(code, body),
			Remedy: CodeModelUnavailable.remedy()}
	}
	return newReason(CodeTestFailed, CodeBadStatus.message(code, body))
}

// timeoutOrFailure names what actually went wrong. "It refused" and "it never answered"
// are different problems with different next moves, so they get different codes.
func timeoutOrFailure(ctx context.Context, err error, budget time.Duration) Reason {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		os.IsTimeout(err) {
		return newReason(CodeTestTimeout, budget.String())
	}
	return newReason(CodeTestFailed, trim(err.Error()))
}

// probeEmbeddings fires one minimal embeddings request.
func (e *Engine) probeEmbeddings(ctx context.Context, spec provider.Spec, baseURL, key, modelID string) (bool, Reason) {
	body, _ := json.Marshal(map[string]any{"model": modelID, "input": "hi"})
	budget := probeBudget(spec)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	code, raw, err := e.httpDo(ctx, spec, http.MethodPost, provider.URL(baseURL, "embeddings"), key, body)
	if err != nil {
		return false, timeoutOrFailure(ctx, err, budget)
	}
	if code == http.StatusOK {
		return true, Reason{Code: CodeOK}
	}
	return false, classifyRefusal(code, trimKnown(string(raw), key))
}

// secretish matches the shapes provider keys come in. Broad on purpose: a slightly
// vaguer error message costs nothing, and a key copied into `sources.status_reason`
// costs everything.
var secretish = regexp.MustCompile(
	`(?i)\b(?:sk-[A-Za-z0-9_\-]{8,}|gsk_[A-Za-z0-9_\-]{8,}|r8_[A-Za-z0-9_\-]{8,}|` +
		`nvapi-[A-Za-z0-9_\-]{8,}|` +
		`frl_[A-Za-z0-9_\-]{8,}|Bearer\s+[A-Za-z0-9._\-]{12,}|` +
		`(?:api[-_]?key|authorization|x-api-key)["\'\s:=]+[A-Za-z0-9._\-]{12,})`)

// trim bounds an upstream message and strips anything key-shaped out of it first. These
// strings are persisted, and they are written by the provider — a provider that echoes
// the credential it received would otherwise have Ferrule store it.
//
// Shape matching is a net, not a guarantee: an opaque key matches nothing. Callers that
// hold the actual key use trimKnown so the literal value goes first.
func trim(s string) string {
	s = secretish.ReplaceAllString(s, "[redacted]")
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}
