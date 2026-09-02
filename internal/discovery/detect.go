package discovery

import (
	"context"
	"ferrule/internal/i18n"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"ferrule/internal/provider"
	"ferrule/internal/store"
)

// Detection asks two different questions and gives each the budget it deserves.
//
// "Is anything listening?" is a TCP question and answers in single-digit milliseconds,
// so dialTimeout is short and nothing waits on a dead port. "What models do you have?"
// is an HTTP question that a cold runtime can be slow to answer — a freshly woken Ollama
// takes well over a second to serve its first /v1/models while subsequent calls take
// twenty milliseconds — so listTimeout is generous. Nothing is blocked on it: detection
// runs in the background at daemon start and the panel paints regardless.
//
// Collapsing the two into one short budget is what makes a runtime that is plainly
// running report as "not detected", intermittently, which is worse than not detecting it
// at all.
const (
	dialTimeout = 400 * time.Millisecond
	listTimeout = 10 * time.Second
)

// Detect scans localhost for known local runtimes and adopts what it finds, with zero
// input from the person. It never downloads anything.
func (e *Engine) Detect(ctx context.Context) ([]Result, error) {
	e.beginScan()
	defer e.endScan()
	type hit struct {
		spec provider.Spec
		url  string
	}
	var (
		mu   sync.Mutex
		hits []hit
		wg   sync.WaitGroup
	)
	probe := &http.Client{Timeout: listTimeout}
	for _, spec := range provider.Local() {
		for _, u := range e.detectURLs(spec) {
			wg.Add(1)
			go func(spec provider.Spec, u string) {
				defer wg.Done()
				if !listening(ctx, u) {
					return
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.URL(u, "models"), nil)
				if err != nil {
					return
				}
				resp, err := probe.Do(req)
				if err != nil {
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return
				}
				mu.Lock()
				hits = append(hits, hit{spec, u})
				mu.Unlock()
			}(spec, u)
		}
	}
	wg.Wait()

	if len(hits) > 0 {
		e.step("detect", "", i18n.T("step.detected", len(hits)))
	}
	var out []Result
	for _, h := range hits {
		// A detected runtime keeps the source it already has; re-detection refreshes it.
		if existing, err := e.db.SourceByName(h.spec.ID); err == nil && existing.BaseURL == h.url {
			r, err := e.Refresh(ctx, existing.ID)
			if err != nil {
				continue
			}
			out = append(out, e.softenEmpty(h.spec, r))
			continue
		}
		r, err := e.Add(ctx, AddRequest{
			Name: h.spec.ID, Provider: h.spec.ID, BaseURL: h.url, Detected: true,
		})
		if err != nil {
			continue
		}
		out = append(out, e.softenEmpty(h.spec, r))
	}
	return out, nil
}

// listening reports whether anything holds the port. A closed port answers in about two
// milliseconds, so this is what keeps a scan of five dead ports instant.
func listening(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// detectURLs returns the URLs scanned for a local runtime, honouring any override. The
// override is how a person points Ferrule at a runtime on a non-standard port, and how
// the harness stands a runtime up without squatting on the real ones.
func (e *Engine) detectURLs(spec provider.Spec) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if urls, ok := e.detectOverride[spec.ID]; ok {
		return urls
	}
	return spec.DetectURLs
}

// SetDetectURLs overrides where local detection looks for one provider.
func (e *Engine) SetDetectURLs(providerID string, urls []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.detectOverride == nil {
		e.detectOverride = map[string][]string{}
	}
	e.detectOverride[providerID] = urls
}

// softenEmpty rewrites the generic "no models" failure for a detected runtime that is
// plainly running but empty — a state the person fixes by pulling a model, not by
// re-pasting a key, and one Ferrule must never fix by downloading gigabytes uninvited.
func (e *Engine) softenEmpty(spec provider.Spec, r Result) Result {
	if r.Source.Status != store.StatusFailed || r.Reason.Code != CodeNoModels {
		return r
	}
	reason := newReason(CodeLocalNoModels, spec.Label)
	r.Source = e.fail(r.Source, reason)
	r.Reason = reason
	return r
}
