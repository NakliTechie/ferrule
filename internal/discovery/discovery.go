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
	"net/http"
	"sort"
	"strings"
	"sync"

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
	scanning       int
	onStep         func(Step)
}

// Step is a progress report from the pipeline. Adding a source can take a minute when a
// local runtime has to load a model to answer the test request, and a minute of silence
// reads as a hang. Callers that have a person waiting on them subscribe to these.
type Step struct {
	Phase  string `json:"phase"` // detect | probe | classify | test
	Source string `json:"source"`
	Note   string `json:"note"`
}

// OnStep registers a progress sink. Not safe to change while a pipeline is running.
func (e *Engine) OnStep(f func(Step)) { e.onStep = f }

func (e *Engine) step(phase, source, note string) {
	if e.onStep != nil {
		e.onStep(Step{Phase: phase, Source: source, Note: note})
	}
}

// Scanning reports whether a detection pass is in flight, so a surface can say "still
// looking" instead of guessing on a timer and announcing "nothing found" too early.
func (e *Engine) Scanning() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.scanning > 0
}

func (e *Engine) beginScan() {
	e.mu.Lock()
	e.scanning++
	e.mu.Unlock()
}

func (e *Engine) endScan() {
	e.mu.Lock()
	if e.scanning > 0 {
		e.scanning--
	}
	e.mu.Unlock()
}

// New builds an Engine.
func New(db *store.DB, v vault.Vault, cat *catalog.Catalog) *Engine {
	return &Engine{
		db: db, vault: v, cat: cat,
		// No client-level timeout: every call sets its own deadline on the context, and
		// they differ by an order of magnitude (a listing is seconds, a cold local model
		// load is minutes). A client timeout would silently override the longer ones and
		// then the reported budget would be a lie.
		client: &http.Client{},
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
	// Reason is the typed failure when Source.Status is failed: a code to branch on, a
	// message for a person, and the exact next move.
	Reason Reason `json:"reason,omitzero"`
}

// Add runs the whole pipeline for one source. A failure is persisted as a `failed`
// source carrying its reason — never dropped, never silently stored as working.
func (e *Engine) Add(ctx context.Context, req AddRequest) (Result, error) {
	spec, ok := provider.Get(req.Provider)
	if !ok {
		return Result{}, newReason(CodeUnknownProvider, req.Provider)
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = spec.DefaultBaseURL
	}
	if baseURL == "" {
		return Result{}, newReason(CodeNeedsBaseURL)
	}
	key := strings.TrimSpace(req.Key)
	if spec.NeedsKey && key == "" {
		return Result{}, newReason(CodeNeedsKey, spec.Label)
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
	if !reason.OK() {
		src = e.fail(src, reason)
		return Result{Source: src, Reason: reason}, nil
	}
	if err := e.db.ReplaceModels(src.ID, res); err != nil {
		return Result{}, err
	}
	src = e.live(src, len(res))
	return Result{Source: src, Models: res}, nil
}

// fail persists a typed failure. A dead source is kept, visibly dead, with the reason
// and the remedy that go with it — never dropped, never stored as working.
func (e *Engine) fail(src store.Source, r Reason) store.Source {
	_ = e.db.SetSourceStatus(src.ID, store.StatusFailed, r.Code_(), r.Message, r.Remedy)
	src.Status, src.StatusCode, src.StatusReason, src.StatusRemedy =
		store.StatusFailed, r.Code_(), r.Message, r.Remedy
	return src
}

func (e *Engine) live(src store.Source, n int) store.Source {
	msg := CodeOK.message(n)
	_ = e.db.SetSourceStatus(src.ID, store.StatusLive, string(CodeOK), msg, "")
	src.Status, src.StatusCode, src.StatusReason, src.StatusRemedy =
		store.StatusLive, string(CodeOK), msg, ""
	return src
}

// run is probe → classify → test. A reason that is not OK means the source stays dark.
func (e *Engine) run(ctx context.Context, spec provider.Spec, src store.Source, key string) ([]store.Model, Reason) {
	e.step("probe", src.Name, i18n.T("step.probe", src.BaseURL))
	listed, err := e.list(ctx, spec, src.BaseURL, key)
	if err != nil {
		if r, ok := err.(Reason); ok {
			return nil, r
		}
		return nil, reasonf(CodeUnreachable, err.Error())
	}
	e.step("classify", src.Name, i18n.T("step.classify", len(listed)))
	models := e.classify(ctx, spec, src, key, listed)

	// test — one minimal real request. Passthrough sources were tested by their
	// authenticated listing call, which is the honest equivalent for that lane.
	if spec.Lane == store.LaneTokens {
		note := i18n.T("step.test")
		if spec.Kind == store.KindLocal {
			note = i18n.T("step.testLocal")
		}
		e.step("test", src.Name, note)
		if reason := e.test(ctx, spec, src, key, models); !reason.OK() {
			return nil, reason
		}
	}
	return models, Reason{Code: CodeOK}
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
		} else if caps, ok := e.db.Learned(lm.ID); ok {
			// A probe already paid for this answer once (DRIVER §8).
			m.Capabilities = caps
			m.ClassifiedBy = i18n.T("classify.learned")
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
			// The ratchet: what this request cost is not spent again.
			_ = e.db.Learn(id, caps)
		}(idx)
	}
	wg.Wait()
	return out
}

// test fires one minimal real request against the source's most representative model.
func (e *Engine) test(ctx context.Context, spec provider.Spec, src store.Source, key string, models []store.Model) Reason {
	chat, embed := pickTestModels(models)

	// On a local runtime the test doubles as a model load, so prefer an embeddings model
	// when one is available: it wakes in a fraction of the time an 8B chat model takes,
	// and it proves the same thing — that this source actually serves.
	order := []struct {
		model string
		probe func(context.Context, provider.Spec, string, string, string) (bool, Reason)
	}{
		{chat, e.probeChat}, {embed, e.probeEmbeddings},
	}
	if spec.Kind == store.KindLocal && embed != "" {
		order[0], order[1] = order[1], order[0]
	}

	var last Reason
	tried := false
	for _, o := range order {
		if o.model == "" {
			continue
		}
		tried = true
		if ok, why := o.probe(ctx, spec, src.BaseURL, key, o.model); ok {
			return Reason{Code: CodeOK}
		} else {
			last = why
		}
	}
	if !tried {
		return newReason(CodeNoModels)
	}
	if key == "" {
		return last.localRemedy()
	}
	return last
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
		return Result{}, newReason(CodeUnknownProvider, src.Provider)
	}
	key := ""
	if src.KeyRef != "" {
		if key, err = e.vault.Get(src.KeyRef); err != nil {
			return Result{}, err
		}
	}
	models, reason := e.run(ctx, spec, src, key)
	if !reason.OK() {
		src = e.fail(src, reason)
		return Result{Source: src, Reason: reason}, nil
	}
	if err := e.db.ReplaceModels(src.ID, models); err != nil {
		return Result{}, err
	}
	src = e.live(src, len(models))
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
