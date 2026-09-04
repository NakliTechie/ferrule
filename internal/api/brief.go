package api

import (
	"context"
	"sort"

	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/discovery"
	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/store"
)

// briefFailures and briefCalls bound the two history windows. The brief's size grows
// with the number of sources and aliases a person has configured — never with the model
// catalog, the ledger, or the control log (DRIVER §4).
const (
	briefFailures = 5
	briefCalls    = 5
	briefStaged   = 10
	briefEgress   = 10
)

// brief is Ferrule's single perception act: one bounded read that renders the whole
// situation, so nobody's — and no agent's — first move has to be exploration.
func brief(ctx context.Context, a *app.App, endpoint string) (any, error) {
	srcs, err := a.DB.Sources()
	if err != nil {
		return nil, err
	}
	models, err := a.DB.Models("")
	if err != nil {
		return nil, err
	}
	aliases, err := a.DB.Aliases()
	if err != nil {
		return nil, err
	}
	grants, err := a.DB.Grants()
	if err != nil {
		return nil, err
	}
	staged, err := a.DB.StagedOps()
	if err != nil {
		return nil, err
	}

	live := map[string]store.Source{}
	sourceRows := make([]map[string]any, 0, len(srcs))
	byID := map[string]store.Source{}
	for _, s := range srcs {
		byID[s.ID] = s
		if s.Status == store.StatusLive {
			live[s.ID] = s
		}
		row := map[string]any{
			"id": s.ID, "name": s.Name, "provider": s.Provider, "where": s.Kind,
			"lane": s.Lane, "status": s.Status, "code": s.StatusCode,
		}
		if s.Status != store.StatusLive {
			row["reason"], row["remedy"] = s.StatusReason, s.StatusRemedy
		}
		sourceRows = append(sourceRows, row)
	}

	// Models are summarised, never listed: the list is what `list_models` is for, and a
	// brief that grew with the catalog would stop being a brief.
	caps := map[string]int{}
	where := map[string]int{"local": 0, "cloud": 0}
	servable := 0
	for _, m := range models {
		s, ok := live[m.SourceID]
		if !ok || s.Lane != store.LaneTokens {
			continue
		}
		servable++
		where[s.Kind]++
		for _, c := range m.Capabilities {
			caps[c]++
		}
	}

	aliasRows := make([]map[string]any, 0, len(aliases))
	for _, al := range aliases {
		serving, dead := "", 0
		for _, r := range al.Rungs {
			s, ok := live[r.SourceID]
			if !ok {
				dead++
				continue
			}
			if serving == "" {
				serving = s.Name + "/" + r.ModelID
			}
		}
		row := map[string]any{"name": al.Name, "rungs": len(al.Rungs), "serving": serving}
		if dead > 0 {
			row["dead_rungs"] = dead
		}
		if serving == "" {
			row["serving"] = nil
			row["code"] = "exhausted"
		}
		aliasRows = append(aliasRows, row)
	}

	liveGrants := 0
	for _, g := range grants {
		if !g.Revoked() {
			liveGrants++
		}
	}

	egress, err := egressReport(a, 24)
	if err != nil {
		return nil, err
	}
	// The per-model egress breakdown grows with the number of distinct models used; the
	// brief keeps only the head of it. `egress_summary` is the unbounded read.
	if m, ok := egress.(map[string]any); ok {
		if detail, ok := m["detail"].([]store.Bucket); ok && len(detail) > briefEgress {
			m["detail"] = detail[:briefEgress]
			m["detail_truncated"] = true
		}
	}

	// The failing rows are asked for directly. Scanning a fixed window of recent entries
	// for them loses a failure the moment enough successes follow it — which is exactly
	// when someone is looking for it.
	failed, err := a.DB.Failures(briefFailures)
	if err != nil {
		return nil, err
	}
	failures := make([]map[string]any, 0, len(failed))
	for _, e := range failed {
		failures = append(failures, map[string]any{
			"ts": e.TS, "app": e.App, "model": e.ModelID, "status": e.Status, "error": e.Err,
		})
	}

	// The tool holds the trajectory, not the agent's head (DRIVER §7).
	calls, err := a.DB.ControlLog(briefCalls)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"endpoint":        endpoint,
		"config_dir":      a.Dir,
		"vault":           a.Vault.Backend(),
		"catalog":         map[string]any{"date": a.Catalog.Date(), "stale": a.Catalog.Stale()},
		"sources":         sourceRows,
		"models":          map[string]any{"servable": servable, "by_where": where, "by_capability": caps},
		"aliases":         aliasRows,
		"grants":          map[string]any{"live": liveGrants, "total": len(grants)},
		"staged":          stagedIDs(staged),
		"egress_24h":      egress,
		"failures":        failures,
		"recent_calls":    calls,
		"content_logging": a.DB.Setting(store.SetContentLogging, "off"),
		"next":            nextActions(a, srcs, aliases, liveGrants, len(staged), endpoint),
		"reason_codes":    codeStrings(),
	}, nil
}

// stagedIDs renders at most briefStaged entries. A brief that grew with the staging
// table would stop being a brief the first time an agent staged a thousand operations.
func stagedIDs(ops []store.StagedOp) []map[string]any {
	if len(ops) > briefStaged {
		ops = ops[:briefStaged]
	}
	out := make([]map[string]any, 0, len(ops))
	for _, o := range ops {
		out = append(out, map[string]any{"id": o.ID, "op": o.Op, "door": o.Door, "caller": o.Caller})
	}
	return out
}

func codeStrings() []string {
	cs := discovery.Codes()
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// nextActions turns the current state into the small set of moves that would change it.
// Every dark source contributes its own remedy, so the documentation for a failure is
// delivered where the failure is read (DRIVER §5).
func nextActions(a *app.App, srcs []store.Source, aliases []store.Alias, liveGrants, staged int, endpoint string) []string {
	var out []string
	anyLive := false
	for _, s := range srcs {
		if s.Status == store.StatusLive {
			anyLive = true
		}
	}
	if !anyLive {
		out = append(out, i18n.T("next.addSource"))
	}
	for _, s := range srcs {
		if s.Status == store.StatusLive {
			continue
		}
		remedy := s.StatusRemedy
		if remedy == "" {
			// A source recorded before the reason vocabulary existed has a message but
			// no remedy. Re-probing it produces both.
			remedy = i18n.T("next.reprobe")
		}
		out = append(out, i18n.T("next.fixSource", s.Name, s.StatusReason, remedy))
	}
	if staged > 0 {
		out = append(out, i18n.T("next.applyStaged", staged))
	}
	if anyLive && liveGrants == 0 {
		out = append(out, i18n.T("next.mintGrant", endpoint))
	}
	if anyLive && len(aliases) == 0 {
		out = append(out, i18n.T("next.setAlias"))
	}
	if a.Catalog.Stale() {
		out = append(out, i18n.T("next.catalogStale"))
	}
	if len(out) == 0 {
		out = append(out, i18n.T("next.nothing"))
	}
	return out
}
