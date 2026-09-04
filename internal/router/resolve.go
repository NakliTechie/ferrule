package router

import (
	"errors"
	"fmt"

	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/store"
)

// Target is one rung the router may try: a live source and a model on it.
type Target struct {
	Source store.Source
	Model  store.Model
	// Rung is the ladder position this target came from, 0 for a direct hit.
	Rung int
	// Via names how the request got here, for the ledger and for error messages.
	Via string
}

// ErrNoRoute is returned when a requested name matches nothing.
var ErrNoRoute = errors.New("router: no route")

// Resolve turns the `model` field of a request into an ordered list of candidates.
// The field may be an alias, a remapped id, a bare model id, or "source/model".
// Aliases resolve to their whole ladder so a dead rung degrades to the next one rather
// than erroring (§4.6 error state).
func (r *Router) Resolve(name string) ([]Target, error) {
	if name == "" {
		return nil, fmt.Errorf("%s", i18n.T("route.unknownModel", name))
	}
	// Two things must not be confused here, and confusing them is how a prompt ends up
	// somewhere it was never pointed.
	//
	// "There is no alias by that name" means: keep looking. "I could not read the alias
	// table" means: stop. A storage fault that reads as an absence walks straight past
	// the person's routing and lands on a bare model id that happens to match — which is
	// the exhausted-alias bug again, arriving through a different door. Ferrule's whole
	// claim is about where prompts go, so an unreadable database is a refusal, never a
	// guess.
	//
	// Only store.ErrNotFound advances the search. Everything else returns.
	if _, err := r.db.Alias(name); err == nil {
		return r.fromAlias(name, "alias")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, unreadable(name, err)
	}

	rm, err := r.db.Remap(name)
	if err == nil {
		if _, aerr := r.db.Alias(rm.Target); aerr == nil {
			return r.fromAlias(rm.Target, "remap→alias")
		} else if !errors.Is(aerr, store.ErrNotFound) {
			return nil, unreadable(name, aerr)
		}
		if sid, mid, ok := store.SplitTarget(rm.Target); ok {
			t, derr := r.direct(sid, mid, "remap")
			if derr != nil {
				return nil, fmt.Errorf("%s: %w", i18n.T("route.remapDark", name, rm.Target), derr)
			}
			return []Target{t}, nil
		}
		return nil, fmt.Errorf("%s", i18n.T("route.remapDark", name, rm.Target))
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, unreadable(name, err)
	}

	if sid, mid, ok := store.SplitTarget(name); ok {
		// Accept both the source id and its display name on the left of the slash.
		t, derr := r.direct(sid, mid, "explicit")
		if derr == nil {
			return []Target{t}, nil
		}
		if !errors.Is(derr, store.ErrNotFound) && !errors.Is(derr, ErrSourceDark) {
			return nil, unreadable(name, derr)
		}
		s, serr := r.db.SourceByName(sid)
		if serr == nil {
			t, derr := r.direct(s.ID, mid, "explicit")
			if derr == nil {
				return []Target{t}, nil
			}
			return nil, fmt.Errorf("%s: %w", i18n.T("route.unknownModel", name), derr)
		}
		if !errors.Is(serr, store.ErrNotFound) {
			return nil, unreadable(name, serr)
		}
	}

	m, s, err := r.db.FindModel(name)
	if err == nil {
		return []Target{{Source: s, Model: m, Via: "model"}}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, unreadable(name, err)
	}
	return nil, fmt.Errorf("%s: %w", i18n.T("route.unknownModel", name), ErrNoRoute)
}

// ErrUnreadable means routing could not be decided because the store could not be read.
// It is deliberately distinct from ErrNoRoute: one says "you asked for something that
// does not exist", the other says "I cannot tell, so I will not send this anywhere".
var ErrUnreadable = errors.New("router: routing table unreadable")

func unreadable(name string, err error) error {
	return fmt.Errorf("%s: %w: %v", i18n.T("route.unreadable", name), ErrUnreadable, err)
}

// ErrSourceDark means the source exists and is not live. It is a legitimate reason to
// keep looking for another way to serve a name — unlike an unreadable store, which is
// not — so it needs to be distinguishable without matching on a localized message.
var ErrSourceDark = errors.New("router: source not live")

func (r *Router) fromAlias(name, via string) ([]Target, error) {
	a, err := r.db.Alias(name)
	if err != nil {
		return nil, err
	}
	var out []Target
	for i, rung := range a.Rungs {
		t, err := r.direct(rung.SourceID, rung.ModelID, via)
		if err != nil {
			// A rung that is dark or gone is what the ladder is for. A database fault is
			// not: skipping past one would send the prompt to a different provider than
			// the person configured, and the only sign would be the answer coming back in
			// a different voice.
			if errors.Is(err, ErrSourceDark) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		t.Rung = i
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s", i18n.T("alias.exhausted", name))
	}
	return out, nil
}

func (r *Router) direct(sourceID, modelID, via string) (Target, error) {
	s, err := r.db.Source(sourceID)
	if err != nil {
		return Target{}, err
	}
	if s.Status != store.StatusLive {
		return Target{}, fmt.Errorf("%s: %w",
			i18n.T("route.sourceNotLive", s.Name, i18n.SourceStatus(s.Status)), ErrSourceDark)
	}
	m, err := r.db.Model(sourceID, modelID)
	if err != nil {
		// A live source may serve a model the last probe did not list (a freshly pulled
		// local model, say). Route it anyway and let the upstream be the judge.
		m = store.Model{SourceID: sourceID, ModelID: modelID}
	}
	return Target{Source: s, Model: m, Via: via}, nil
}
