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
	testModel      string
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
		//
		// Redirects are refused outright: every request this client makes carries a
		// provider key, and following a redirect would hand that key to whatever host
		// the response names. Go strips Authorization across hosts but not a custom
		// header like Anthropic's x-api-key, so relying on that would be relying on the
		// wrong guarantee.
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

// AddRequest is one trip through the pipeline. Key is empty for detected local sources.
type AddRequest struct {
	Name     string
	Provider string
	BaseURL  string
	Key      string
	Detected bool
	// AllowInsecure acknowledges that this source's key will travel over http to a host
	// that is not this machine. It is never a default and it is recorded on the source.
	AllowInsecure bool
	// TestModel names the model the test step should use, for an account whose tier does
	// not include whichever ones Ferrule would have picked.
	TestModel string
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
	if r := checkEndpoint(baseURL, key != ""); !r.OK() && !req.AllowInsecure {
		return Result{}, r
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
		Insecure: !checkEndpoint(baseURL, key != "").OK(),
	}
	if key != "" {
		src.KeyRef = vault.Ref(id)
	}
	// The key goes to the vault first, and the row that references it second.
	//
	// Two stores, one operation, and no transaction across them — so the order decides
	// which half-failure is possible. Vault-then-row means a failure leaves an orphaned
	// encrypted blob that nothing points at, which the startup sweep collects and which
	// no user-visible surface is wrong about. Row-then-vault means a source that appears
	// live and holds a reference to a key that was never stored, which is a lie the
	// interface tells until someone tries to use it.
	if key != "" {
		if err := e.vault.Put(src.KeyRef, key); err != nil {
			return Result{}, err
		}
	}
	if err := e.db.PutSource(src); err != nil {
		// Take the orphan back out rather than leaving it for the sweep.
		if key != "" {
			_ = e.vault.Delete(src.KeyRef)
		}
		return Result{}, err
	}

	e.mu.Lock()
	e.testModel = req.TestModel
	e.mu.Unlock()
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
		if ent, ok := e.cat.Lookup(src.Provider, lm.ID); ok {
			m.Capabilities = ent.Capabilities
			m.Modalities = ent.Modalities
			m.Async = ent.Async
			m.InCost, m.OutCost = ent.InCost, ent.OutCost
			if m.ContextLength == 0 {
				m.ContextLength = ent.Context
			}
			m.ClassifiedBy = i18n.T("classify.catalogHit")
		} else if caps, ok := e.db.Learned(src.Provider, lm.ID); ok {
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
	// An account-level refusal answers for every model, so the first one cancels the rest.
	// Without this, a provider that has simply run out of credit is asked the same
	// question a dozen times — two probes per unknown model — before the test step gets
	// to say so once.
	probeCtx, stopProbing := context.WithCancel(ctx)
	defer stopProbing()

	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, idx := range unknown {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := out[idx].ModelID
			var caps []string
			ok, why := e.probeChat(probeCtx, spec, src.BaseURL, key, id)
			if !ok && accountLevel(why.Code) {
				stopProbing()
				return
			}
			if ok {
				caps = []string{"chat"}
			} else if ok2, why2 := e.probeEmbeddings(probeCtx, spec, src.BaseURL, key, id); ok2 {
				caps = []string{"embeddings"}
			} else if accountLevel(why2.Code) {
				stopProbing()
				return
			}
			if caps == nil {
				return
			}
			mu.Lock()
			out[idx].Capabilities = caps
			out[idx].ClassifiedBy = i18n.T("classify.liveProbe")
			mu.Unlock()
			// The ratchet: what this request cost is not spent again.
			_ = e.db.Learn(src.Provider, id, caps)
		}(idx)
	}
	wg.Wait()
	return out
}

// maxTestModels bounds how many models the test step will try before giving up on a
// source. Each attempt is a real request with max_tokens 1 — fractions of a cent — and
// stopping at one was condemning a whole account for the sample of one it happened to
// pick.
const maxTestModels = 4

// test fires minimal real requests until one succeeds, or until the provider says
// something that no other model would change.
//
// The one-model version marked a real NVIDIA account dead: it listed 81 models, Ferrule
// tested the first, that model was outside the account's tier, and 80 working models went
// with it. A 404 is about a model. A 401 or a 402 is about the account, and trying
// another model would only spend more requests learning the same thing.
func (e *Engine) test(ctx context.Context, spec provider.Spec, src store.Source, key string, models []store.Model) Reason {
	e.mu.RLock()
	named := e.testModel
	e.mu.RUnlock()

	candidates := testCandidates(spec, models)
	if named != "" {
		// A named model is the only one tried: the person is telling Ferrule which one
		// their account can call, and guessing past it would waste requests.
		candidates = []testCandidate{{model: named, embeddings: false}}
		for _, m := range models {
			if m.ModelID == named && hasCap(m, "embeddings") {
				candidates[0].embeddings = true
			}
		}
	}
	if len(candidates) == 0 {
		return newReason(CodeNoModels)
	}

	var last Reason
	tried := 0
	for _, c := range candidates {
		tried++
		probe := e.probeChat
		if c.embeddings {
			probe = e.probeEmbeddings
		}
		ok, why := probe(ctx, spec, src.BaseURL, key, c.model)
		if ok {
			return Reason{Code: CodeOK}
		}
		last = why
		// Account-level answers apply to every model, so stop asking.
		switch why.Code {
		case CodeBadKey, CodeNoBalance, CodeTestTimeout:
			if key == "" {
				return why.localRemedy()
			}
			return why
		}
	}

	if last.Code == CodeModelUnavailable {
		// Every model tried was refused individually. Say how many, so "not available"
		// does not read as "we barely looked".
		return newReason(CodeModelUnavailable, tried, http.StatusNotFound, last.Message)
	}
	if key == "" {
		return last.localRemedy()
	}
	return last
}

// accountLevel reports whether a refusal answers for the whole account rather than for
// the model that happened to be asked. Those need asking once.
func accountLevel(c Code) bool {
	return c == CodeBadKey || c == CodeNoBalance
}

// testCandidate is one model the test step may try.
type testCandidate struct {
	model      string
	embeddings bool
}

// testCandidates orders what to try. Models the catalog recognised come first — a known
// id is far likelier to be one the account can actually call than an unclassified one —
// then cheapest first. On a local runtime an embeddings model leads, because it wakes in
// a fraction of the time a chat model takes and proves the same thing.
func testCandidates(spec provider.Spec, models []store.Model) []testCandidate {
	sorted := append([]store.Model(nil), models...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		ak, bk := a.ClassifiedBy != i18n.T("classify.unknown"), b.ClassifiedBy != i18n.T("classify.unknown")
		if ak != bk {
			return ak
		}
		if a.InCost != b.InCost {
			return a.InCost < b.InCost
		}
		return a.ModelID < b.ModelID
	})

	var chat, embed []testCandidate
	for _, m := range sorted {
		switch {
		case hasCap(m, "embeddings"):
			embed = append(embed, testCandidate{m.ModelID, true})
		case hasCap(m, "chat") || len(m.Capabilities) == 0:
			chat = append(chat, testCandidate{m.ModelID, false})
		}
	}

	var out []testCandidate
	if spec.Kind == store.KindLocal && len(embed) > 0 {
		out = append(out, embed[0])
	}
	out = append(out, chat...)
	out = append(out, embed...)
	if len(out) > maxTestModels {
		out = out[:maxTestModels]
	}
	return out
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
		// The key goes first, for the same reason it is written first: the failure this
		// ordering allows is a row whose key is already gone, which shows up immediately
		// and honestly as a source that cannot authenticate. The other ordering allows a
		// secret to survive on disk with nothing in the interface referring to it.
		if err := e.vault.Delete(src.KeyRef); err != nil {
			return err
		}
	}
	return e.db.DeleteSource(sourceID)
}
