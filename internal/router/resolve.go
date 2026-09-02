package router

import (
	"errors"
	"fmt"

	"ferrule/internal/i18n"
	"ferrule/internal/store"
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
	if ts, err := r.fromAlias(name, "alias"); err == nil {
		return ts, nil
	}
	if rm, err := r.db.Remap(name); err == nil {
		if ts, err := r.fromAlias(rm.Target, "remap→alias"); err == nil {
			return ts, nil
		}
		if sid, mid, ok := store.SplitTarget(rm.Target); ok {
			if t, err := r.direct(sid, mid, "remap"); err == nil {
				return []Target{t}, nil
			}
		}
	}
	if sid, mid, ok := store.SplitTarget(name); ok {
		// Accept both the source id and its display name on the left of the slash.
		if t, err := r.direct(sid, mid, "explicit"); err == nil {
			return []Target{t}, nil
		}
		if s, err := r.db.SourceByName(sid); err == nil {
			if t, err := r.direct(s.ID, mid, "explicit"); err == nil {
				return []Target{t}, nil
			}
		}
	}
	m, s, err := r.db.FindModel(name)
	if err == nil {
		return []Target{{Source: s, Model: m, Via: "model"}}, nil
	}
	return nil, fmt.Errorf("%s: %w", i18n.T("route.unknownModel", name), ErrNoRoute)
}

func (r *Router) fromAlias(name, via string) ([]Target, error) {
	a, err := r.db.Alias(name)
	if err != nil {
		return nil, err
	}
	var out []Target
	for i, rung := range a.Rungs {
		t, err := r.direct(rung.SourceID, rung.ModelID, via)
		if err != nil {
			continue
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
		return Target{}, fmt.Errorf("%s", i18n.T("route.sourceNotLive", s.Name, s.Status))
	}
	m, err := r.db.Model(sourceID, modelID)
	if err != nil {
		// A live source may serve a model the last probe did not list (a freshly pulled
		// local model, say). Route it anyway and let the upstream be the judge.
		m = store.Model{SourceID: sourceID, ModelID: modelID}
	}
	return Target{Source: s, Model: m, Via: via}, nil
}
