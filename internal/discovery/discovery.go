// Package discovery is the add-a-source pipeline (§2.3) — the anti-LiteLLM.
//
//	detected (local runtime)  ─┐
//	                           ├─►  probe ─► classify ─► test ─► live (aliasable)
//	pasted (key [+ endpoint])  ─┘                             └─ or FAIL LOUD, with a reason
//
// One mechanism, two doors. A dead key is never silently stored.
package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ferrule/internal/catalog"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
	"ferrule/internal/vault"
)

// maxLiveProbes caps how many unclassified models get a live probe per add. Bounded so
// adding a source with 300 models stays fast and cheap.
const maxLiveProbes = 6

// Engine runs the pipeline against the store, the vault, and the catalog.
type Engine struct {
	db     *store.DB
	vault  vault.Vault
	cat    *catalog.Catalog
	client *http.Client

	mu             sync.RWMutex
	detectOverride map[string][]string
}

// New builds an Engine.
func New(db *store.DB, v vault.Vault, cat *catalog.Catalog) *Engine {
	return &Engine{
		db: db, vault: v, cat: cat,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// AddRequest is one trip through the pipeline. Key is empty for detected local sources.
type AddRequest struct {
	Name     string
	Provider string
	BaseURL  string
	Key      string
	Detected bool
}

// Result is what the pipeline produced, live or failed.
type Result struct {
	Source store.Source  `json:"source"`
	Models []store.Model `json:"models"`
	// Reason is the visible failure reason when Source.Status is failed.
	Reason string `json:"reason,omitempty"`
}

// Add runs the whole pipeline for one source. A failure is persisted as a `failed`
// source carrying its reason — never dropped, never silently stored as working.
func (e *Engine) Add(ctx context.Context, req AddRequest) (Result, error) {
	spec, ok := provider.Get(req.Provider)
	if !ok {
		return Result{}, fmt.Errorf("%s", i18n.T("source.unknownProvider", req.Provider, provider.Names()))
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = spec.DefaultBaseURL
	}
	if baseURL == "" {
		return Result{}, errors.New(i18n.T("source.needBaseURL"))
	}
	key := strings.TrimSpace(req.Key)
	if spec.NeedsKey && key == "" {
		return Result{}, fmt.Errorf("%s", i18n.T("source.needKey", spec.Label))
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = spec.ID
	}
	// A repeated add of the same name updates that source rather than colliding.
	id := ""
	if existing, err := e.db.SourceByName(name); err == nil {
		id = existing.ID
	} else {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return Result{}, err
		}
		id = hex.EncodeToString(b)
	}

	src := store.Source{
		ID: id, Name: name, Provider: spec.ID, Kind: spec.Kind, Lane: spec.Lane,
		BaseURL: baseURL, Status: store.StatusProbing, Detected: req.Detected,
	}
	if key != "" {
		src.KeyRef = vault.Ref(id)
	}
	if err := e.db.PutSource(src); err != nil {
		return Result{}, err
	}
	// The key goes to the vault, never to SQLite. It is written before the probe so a
	// crash mid-probe cannot leave a source pointing at a ref that holds nothing.
	if key != "" {
		if err := e.vault.Put(src.KeyRef, key); err != nil {
			return Result{}, err
		}
	}

	res, reason := e.run(ctx, spec, src, key)
	if reason != "" {
		_ = e.db.SetSourceStatus(src.ID, store.StatusFailed, reason)
		src.Status, src.StatusReason = store.StatusFailed, reason
		return Result{Source: src, Reason: reason}, nil
	}
	if err := e.db.ReplaceModels(src.ID, res); err != nil {
		return Result{}, err
	}
	ok2 := i18n.T("probe.ok", len(res))
	_ = e.db.SetSourceStatus(src.ID, store.StatusLive, ok2)
	src.Status, src.StatusReason = store.StatusLive, ok2
	return Result{Source: src, Models: res}, nil
}

// run is probe → classify → test. A non-empty reason means the source does not go live.
func (e *Engine) run(ctx context.Context, spec provider.Spec, src store.Source, key string) ([]store.Model, string) {
	listed, err := e.list(ctx, spec, src.BaseURL, key)
	if err != nil {
		return nil, err.Error()
	}
	models := e.classify(ctx, spec, src, key, listed)

	// test — one minimal real request. Passthrough sources were tested by their
	// authenticated listing call, which is the honest equivalent for that lane.
	if spec.Lane == store.LaneTokens {
		if reason := e.test(ctx, spec, src, key, models); reason != "" {
			return nil, reason
		}
	}
	return models, ""
}

// classify tags each listed model, catalog first, cheap live probe where it is silent.
func (e *Engine) classify(ctx context.Context, spec provider.Spec, src store.Source, key string, listed []listedModel) []store.Model {
	out := make([]store.Model, len(listed))
	var unknown []int
	for i, lm := range listed {
		m := store.Model{
			SourceID: src.ID, ModelID: lm.ID, DisplayName: lm.Display,
			ContextLength: lm.Context,
		}
		if ent, ok := e.cat.Lookup(lm.ID); ok {
			m.Capabilities = ent.Capabilities
			m.Modalities = ent.Modalities
			m.Async = ent.Async
			m.InCost, m.OutCost = ent.InCost, ent.OutCost
			if m.ContextLength == 0 {
				m.ContextLength = ent.Context
			}
			m.ClassifiedBy = i18n.T("classify.catalogHit")
		} else {
			m.ClassifiedBy = i18n.T("classify.unknown")
			unknown = append(unknown, i)
		}
		if spec.Lane == store.LanePassthrough && len(m.Capabilities) == 0 {
			// A passthrough source's shapes are async by construction.
			m.Async = true
		}
		out[i] = m
	}

	if spec.Lane != store.LaneTokens || len(unknown) == 0 {
		return out
	}
	if len(unknown) > maxLiveProbes {
		unknown = unknown[:maxLiveProbes]
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, idx := range unknown {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := out[idx].ModelID
			var caps []string
			if ok, _ := e.probeChat(ctx, spec, src.BaseURL, key, id); ok {
				caps = []string{"chat"}
			} else if ok, _ := e.probeEmbeddings(ctx, spec, src.BaseURL, key, id); ok {
				caps = []string{"embeddings"}
			}
			if caps == nil {
				return
			}
			mu.Lock()
			out[idx].Capabilities = caps
			out[idx].ClassifiedBy = i18n.T("classify.liveProbe")
			mu.Unlock()
		}(idx)
	}
	wg.Wait()
	return out
}

// test fires one minimal real request against the source's most representative model.
func (e *Engine) test(ctx context.Context, spec provider.Spec, src store.Source, key string, models []store.Model) string {
	chat, embed := pickTestModels(models)
	if chat != "" {
		if ok, why := e.probeChat(ctx, spec, src.BaseURL, key, chat); ok {
			return ""
		} else if embed == "" {
			return i18n.T("probe.testFailed", why)
		}
	}
	if embed != "" {
		if ok, why := e.probeEmbeddings(ctx, spec, src.BaseURL, key, embed); ok {
			return ""
		} else {
			return i18n.T("probe.testFailed", why)
		}
	}
	if chat == "" && embed == "" {
		return i18n.T("probe.testNoModel")
	}
	return i18n.T("probe.testFailed", "")
}

func pickTestModels(models []store.Model) (chat, embed string) {
	sorted := append([]store.Model(nil), models...)
	sort.SliceStable(sorted, func(i, j int) bool {
		// Prefer the cheapest classified model so the test costs as close to nothing
		// as the provider allows.
		return sorted[i].InCost < sorted[j].InCost
	})
	for _, m := range sorted {
		if chat == "" && hasCap(m, "chat") {
			chat = m.ModelID
		}
		if embed == "" && hasCap(m, "embeddings") {
			embed = m.ModelID
		}
	}
	if chat == "" && embed == "" && len(sorted) > 0 {
		chat = sorted[0].ModelID
	}
	return chat, embed
}

func hasCap(m store.Model, c string) bool {
	for _, x := range m.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// Refresh re-runs the pipeline for an existing source, keeping its id and key.
func (e *Engine) Refresh(ctx context.Context, sourceID string) (Result, error) {
	src, err := e.db.Source(sourceID)
	if err != nil {
		return Result{}, err
	}
	spec, ok := provider.Get(src.Provider)
	if !ok {
		return Result{}, fmt.Errorf("%s", i18n.T("source.unknownProvider", src.Provider, provider.Names()))
	}
	key := ""
	if src.KeyRef != "" {
		if key, err = e.vault.Get(src.KeyRef); err != nil {
			return Result{}, err
		}
	}
	models, reason := e.run(ctx, spec, src, key)
	if reason != "" {
		_ = e.db.SetSourceStatus(src.ID, store.StatusFailed, reason)
		src.Status, src.StatusReason = store.StatusFailed, reason
		return Result{Source: src, Reason: reason}, nil
	}
	if err := e.db.ReplaceModels(src.ID, models); err != nil {
		return Result{}, err
	}
	ok2 := i18n.T("probe.ok", len(models))
	_ = e.db.SetSourceStatus(src.ID, store.StatusLive, ok2)
	src.Status, src.StatusReason = store.StatusLive, ok2
	return Result{Source: src, Models: models}, nil
}

// Remove deletes a source, its models, and its key.
func (e *Engine) Remove(sourceID string) error {
	src, err := e.db.Source(sourceID)
	if err != nil {
		return err
	}
	if src.KeyRef != "" {
		_ = e.vault.Delete(src.KeyRef)
	}
	return e.db.DeleteSource(sourceID)
}
