package discovery

import (
	"context"
	"net/http"
	"sync"
	"time"

	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

// detectTimeout is deliberately short: detection runs on every start and must not hold
// the 5 s cold-load bar hostage on a machine where nothing is listening.
const detectTimeout = 1200 * time.Millisecond

// Detect scans localhost for known local runtimes and adopts what it finds, with zero
// input from the person. It never downloads anything.
func (e *Engine) Detect(ctx context.Context) ([]Result, error) {
	type hit struct {
		spec provider.Spec
		url  string
	}
	var (
		mu   sync.Mutex
		hits []hit
		wg   sync.WaitGroup
	)
	probe := &http.Client{Timeout: detectTimeout}
	for _, spec := range provider.Local() {
		for _, u := range e.detectURLs(spec) {
			wg.Add(1)
			go func(spec provider.Spec, u string) {
				defer wg.Done()
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
	if r.Source.Status != store.StatusFailed || r.Reason != i18n.T("probe.listEmpty") {
		return r
	}
	reason := i18n.T("probe.localNoModels", spec.Label)
	_ = e.db.SetSourceStatus(r.Source.ID, store.StatusFailed, reason)
	r.Source.StatusReason, r.Reason = reason, reason
	return r
}
