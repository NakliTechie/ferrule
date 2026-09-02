package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"ferrule/internal/provider"
)

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
	if code != http.StatusOK {
		return nil, newReason(CodeBadStatus, code, trim(string(raw)))
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
		return nil, newReason(CodeBadStatus, code, trim(string(raw)))
	}
	code, raw, err = e.httpDo(ctx, spec, http.MethodGet, provider.URL(baseURL, "collections/text-to-image"), key, nil)
	if err != nil || code != http.StatusOK {
		// The key is good; the catalogue call is a convenience. Report the source live
		// with no models rather than failing a working key.
		return []listedModel{}, nil
	}
	var doc struct {
		Models []struct {
			Owner       string `json:"owner"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []listedModel{}, nil
	}
	out := make([]listedModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.Owner == "" || m.Name == "" {
			continue
		}
		out = append(out, listedModel{ID: m.Owner + "/" + m.Name, Display: m.Description})
	}
	return out, nil
}

// probeChat fires one minimal chat request. Used as the live-probe classifier and as the
// test step for the raw-tokens lane.
func (e *Engine) probeChat(ctx context.Context, spec provider.Spec, baseURL, key, modelID string) (bool, string) {
	body, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
		"stream":     false,
	})
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	code, raw, err := e.httpDo(ctx, spec, http.MethodPost, provider.URL(baseURL, "chat/completions"), key, body)
	if err != nil {
		return false, trim(err.Error())
	}
	if code == http.StatusOK {
		return true, ""
	}
	return false, CodeBadStatus.message(code, trim(string(raw)))
}

// probeEmbeddings fires one minimal embeddings request.
func (e *Engine) probeEmbeddings(ctx context.Context, spec provider.Spec, baseURL, key, modelID string) (bool, string) {
	body, _ := json.Marshal(map[string]any{"model": modelID, "input": "hi"})
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	code, raw, err := e.httpDo(ctx, spec, http.MethodPost, provider.URL(baseURL, "embeddings"), key, body)
	if err != nil {
		return false, trim(err.Error())
	}
	if code == http.StatusOK {
		return true, ""
	}
	return false, CodeBadStatus.message(code, trim(string(raw)))
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}
