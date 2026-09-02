package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ferrule/internal/app"
	"ferrule/internal/discovery"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

func stagedMsg(op string) string        { return i18n.T("mcp.stage.pending", op) }
func personOnlyMsg(op string) string    { return i18n.T("mcp.personOnly", op) }
func stagedAppliedMsg(op string) string { return i18n.T("mcp.stage.applied", op) }
func stagedMissingMsg(id string) string { return i18n.T("mcp.stage.notFound", id) }

func (b *Bus) register() {
	// ---- read ops: registered on load, no setting, never staged ----

	b.add(&Op{Name: "status", Desc: i18n.T("op.status"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			srcs, _ := a.DB.Sources()
			models, _ := a.DB.Models("")
			live := 0
			for _, s := range srcs {
				if s.Status == store.StatusLive {
					live++
				}
			}
			return map[string]any{
				"config_dir": a.Dir, "vault": a.Vault.Backend(),
				"catalog_date": a.Catalog.Date(), "catalog_stale": a.Catalog.Stale(),
				"sources": len(srcs), "sources_live": live, "models": len(models),
				"platform":        runtime.GOOS + "/" + runtime.GOARCH,
				"content_logging": a.DB.Setting(store.SetContentLogging, "off"),
				"sovereignty":     i18n.T("app.sovereignty"),
			}, nil
		}})

	b.add(&Op{Name: "list_sources", Desc: i18n.T("op.list_sources"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			srcs, err := a.DB.Sources()
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(srcs))
			for _, s := range srcs {
				models, _ := a.DB.Models(s.ID)
				out = append(out, map[string]any{
					"id": s.ID, "name": s.Name, "provider": s.Provider, "where": s.Kind,
					"lane": s.Lane, "base_url": s.BaseURL, "status": s.Status,
					"reason": s.StatusReason, "detected": s.Detected, "models": len(models),
					"has_key": s.KeyRef != "",
				})
			}
			return map[string]any{"sources": out}, nil
		}})

	b.add(&Op{Name: "list_models", Desc: i18n.T("op.list_models"),
		Params: []Param{
			{Name: "where", Type: "string", Desc: "local | cloud"},
			{Name: "capability", Type: "string", Desc: "chat, embeddings, vision, image, audio, video, rerank, tools, reasoning"},
			{Name: "max_cost", Type: "number", Desc: "highest input cost per million tokens"},
		},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			srcs, err := a.DB.Sources()
			if err != nil {
				return nil, err
			}
			by := map[string]store.Source{}
			for _, s := range srcs {
				by[s.ID] = s
			}
			models, err := a.DB.Models("")
			if err != nil {
				return nil, err
			}
			where, capf := args.Str("where"), args.Str("capability")
			maxCost, hasMax := args["max_cost"].(float64)
			out := []map[string]any{}
			for _, m := range models {
				s := by[m.SourceID]
				if s.Status != store.StatusLive {
					continue
				}
				if where != "" && s.Kind != where {
					continue
				}
				if capf != "" && !hasCap(m.Capabilities, capf) {
					continue
				}
				if hasMax && m.InCost > maxCost {
					continue
				}
				out = append(out, map[string]any{
					"model": m.ModelID, "source": s.Name, "where": s.Kind, "lane": s.Lane,
					"capabilities": m.Capabilities, "context_length": m.ContextLength,
					"in_cost_per_mtok": m.InCost, "out_cost_per_mtok": m.OutCost,
					"async": m.Async, "classified_by": m.ClassifiedBy,
				})
			}
			return map[string]any{"models": out, "catalog_date": a.Catalog.Date()}, nil
		}})

	b.add(&Op{Name: "list_aliases", Desc: i18n.T("op.list_aliases"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			as, err := a.DB.Aliases()
			if err != nil {
				return nil, err
			}
			return map[string]any{"aliases": decorate(a, as)}, nil
		}})

	b.add(&Op{Name: "get_alias", Desc: i18n.T("op.get_alias"),
		Params: []Param{{Name: "name", Type: "string", Required: true, Desc: "the alias"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			al, err := a.DB.Alias(args.Str("name"))
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("%s", i18n.T("alias.notFound", args.Str("name")))
			}
			if err != nil {
				return nil, err
			}
			return decorate(a, []store.Alias{al})[0], nil
		}})

	b.add(&Op{Name: "list_remaps", Desc: i18n.T("op.list_remaps"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			rs, err := a.DB.Remaps()
			return map[string]any{"remaps": rs}, err
		}})

	b.add(&Op{Name: "usage_summary", Desc: i18n.T("op.usage_summary"),
		Params: []Param{
			{Name: "by", Type: "string[]", Desc: "any of app, model, source, provider, egress"},
			{Name: "since_hours", Type: "number", Desc: "window in hours; 0 for all time"},
		},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			by := args.Strs("by")
			if len(by) == 0 {
				by = []string{"app", "model"}
			}
			return usageReport(a, by, args.Int("since_hours"))
		}})

	b.add(&Op{Name: "egress_summary", Desc: i18n.T("op.egress_summary"),
		Params: []Param{{Name: "since_hours", Type: "number", Desc: "window in hours; 0 for all time"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			return egressReport(a, args.Int("since_hours"))
		}})

	b.add(&Op{Name: "list_grants", Desc: i18n.T("op.list_grants"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			gs, err := a.DB.Grants()
			if err != nil {
				return nil, err
			}
			spend, err := a.DB.Aggregate([]string{"app"}, 0)
			if err != nil {
				return nil, err
			}
			byApp := map[string]store.Bucket{}
			for _, s := range spend {
				byApp[s.App] = s
			}
			out := make([]map[string]any, 0, len(gs))
			for _, g := range gs {
				s := byApp[g.App]
				out = append(out, map[string]any{
					"id": g.ID, "app": g.App, "created_at": g.CreatedAt,
					"revoked": g.Revoked(), "revoked_at": g.RevokedAt,
					"requests": s.Requests, "cost": s.Cost,
				})
			}
			return map[string]any{"grants": out}, nil
		}})

	b.add(&Op{Name: "list_staged", Desc: i18n.T("op.list_staged"),
		run: func(_ context.Context, a *app.App, _ Args) (any, error) {
			ops, err := a.DB.StagedOps()
			return map[string]any{"staged": ops}, err
		}})

	// ---- mutating ops: staged before they land when they arrive through the agent door ----

	b.add(&Op{Name: "add_source", Desc: i18n.T("op.add_source"), Mutating: true,
		Params: []Param{
			{Name: "provider", Type: "string", Required: true, Desc: "one of: " + provider.Names()},
			{Name: "name", Type: "string", Desc: "a name for this source; defaults to the provider id"},
			{Name: "base_url", Type: "string", Desc: "required for an unknown OpenAI-compatible endpoint"},
			{Name: "key", Type: "string", Secret: true, Desc: "the provider key; withheld from staging and supplied by the person at apply time"},
		},
		run: func(ctx context.Context, a *app.App, args Args) (any, error) {
			r, err := a.Discovery.Add(ctx, discovery.AddRequest{
				Name: args.Str("name"), Provider: args.Str("provider"),
				BaseURL: args.Str("base_url"), Key: args.Str("key"),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"source": r.Source, "models": len(r.Models), "reason": r.Reason,
				"live": r.Source.Status == store.StatusLive,
			}, nil
		}})

	b.add(&Op{Name: "refresh_source", Desc: i18n.T("op.refresh_source"), Mutating: true,
		Params: []Param{{Name: "id", Type: "string", Required: true, Desc: "the source id"}},
		run: func(ctx context.Context, a *app.App, args Args) (any, error) {
			r, err := a.Discovery.Refresh(ctx, args.Str("id"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"source": r.Source, "models": len(r.Models), "reason": r.Reason}, nil
		}})

	b.add(&Op{Name: "remove_source", Desc: i18n.T("op.remove_source"), Mutating: true,
		Params: []Param{{Name: "id", Type: "string", Required: true, Desc: "the source id"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			if err := a.Discovery.Remove(args.Str("id")); err != nil {
				return nil, err
			}
			return map[string]any{"removed": args.Str("id"),
				"message": i18n.T("source.removed", args.Str("id"))}, nil
		}})

	b.add(&Op{Name: "detect_local", Desc: i18n.T("op.detect_local"), Mutating: true,
		run: func(ctx context.Context, a *app.App, _ Args) (any, error) {
			res, err := a.Discovery.Detect(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(res))
			for _, r := range res {
				out = append(out, map[string]any{
					"source": r.Source, "models": len(r.Models), "reason": r.Reason})
			}
			return map[string]any{"detected": out}, nil
		}})

	b.add(&Op{Name: "set_alias", Desc: i18n.T("op.set_alias"), Mutating: true,
		Params: []Param{
			{Name: "name", Type: "string", Required: true, Desc: "the alias, e.g. fast, smart, cheap, local"},
			{Name: "ladder", Type: "string[]", Required: true, Desc: `ordered rungs as "source/model"; the first reachable one serves`},
		},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			name := args.Str("name")
			raw := args.Strs("ladder")
			if len(raw) == 0 {
				return nil, errors.New(i18n.T("alias.empty"))
			}
			var rungs []store.Rung
			for _, r := range raw {
				sid, mid, ok := store.SplitTarget(r)
				if !ok {
					return nil, fmt.Errorf("rung %q is not source/model", r)
				}
				if _, err := a.DB.Source(sid); err != nil {
					s, err2 := a.DB.SourceByName(sid)
					if err2 != nil {
						return nil, fmt.Errorf("%s", i18n.T("source.notFound", sid))
					}
					sid = s.ID
				}
				rungs = append(rungs, store.Rung{SourceID: sid, ModelID: mid})
			}
			if err := a.DB.PutAlias(store.Alias{Name: name, Rungs: rungs}); err != nil {
				return nil, err
			}
			al, err := a.DB.Alias(name)
			if err != nil {
				return nil, err
			}
			return decorate(a, []store.Alias{al})[0], nil
		}})

	b.add(&Op{Name: "remove_alias", Desc: i18n.T("op.remove_alias"), Mutating: true,
		Params: []Param{{Name: "name", Type: "string", Required: true, Desc: "the alias"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			if err := a.DB.DeleteAlias(args.Str("name")); err != nil {
				return nil, err
			}
			return map[string]any{"removed": args.Str("name")}, nil
		}})

	b.add(&Op{Name: "set_remap", Desc: i18n.T("op.set_remap"), Mutating: true,
		Params: []Param{
			{Name: "from", Type: "string", Required: true, Desc: "the model id the app insists on sending"},
			{Name: "to", Type: "string", Required: true, Desc: `an alias name, or "source/model"`},
		},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			r := store.Remap{FromModel: args.Str("from"), Target: args.Str("to")}
			if err := a.DB.PutRemap(r); err != nil {
				return nil, err
			}
			return r, nil
		}})

	b.add(&Op{Name: "remove_remap", Desc: i18n.T("op.remove_remap"), Mutating: true,
		Params: []Param{{Name: "from", Type: "string", Required: true, Desc: "the remapped model id"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			if err := a.DB.DeleteRemap(args.Str("from")); err != nil {
				return nil, err
			}
			return map[string]any{"removed": args.Str("from")}, nil
		}})

	b.add(&Op{Name: "revoke_grant", Desc: i18n.T("op.revoke_grant"), Mutating: true,
		Params: []Param{{Name: "id", Type: "string", Required: true, Desc: "the app token id"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			if err := a.DB.RevokeGrant(args.Str("id")); err != nil {
				return nil, fmt.Errorf("%s", i18n.T("grant.notFound", args.Str("id")))
			}
			return map[string]any{"revoked": args.Str("id")}, nil
		}})

	b.add(&Op{Name: "set_setting", Desc: i18n.T("op.set_setting"), Mutating: true,
		Params: []Param{
			{Name: "key", Type: "string", Required: true, Desc: "content_logging | cross_origin"},
			{Name: "value", Type: "string", Required: true, Desc: "on | off"},
		},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			k, v := args.Str("key"), args.Str("value")
			switch k {
			case store.SetContentLogging, store.SetCrossOrigin:
			default:
				return nil, fmt.Errorf("unknown setting %q", k)
			}
			if v != "on" && v != "off" {
				return nil, fmt.Errorf("setting %q takes on or off", k)
			}
			if err := a.DB.SetSetting(k, v); err != nil {
				return nil, err
			}
			return map[string]any{"key": k, "value": v}, nil
		}})

	// ---- person-only ops: never delegable, and named as such rather than hidden ----

	b.add(&Op{Name: "mint_grant", Desc: i18n.T("op.mint_grant"), Mutating: true, PersonOnly: true,
		Params: []Param{{Name: "app", Type: "string", Required: true, Desc: "the app this token identifies"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			g, tok, err := a.DB.MintGrant(args.Str("app"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"grant": g, "token": tok, "shown_once": true}, nil
		}})

	b.add(&Op{Name: "export_config", Desc: i18n.T("op.export_config"), Mutating: true, PersonOnly: true,
		Params: []Param{{Name: "path", Type: "string", Required: true, Desc: "where to write the portable file"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			return exportConfig(a, args.Str("path"))
		}})

	b.add(&Op{Name: "import_config", Desc: i18n.T("op.import_config"), Mutating: true, PersonOnly: true,
		Params: []Param{{Name: "path", Type: "string", Required: true, Desc: "the portable file to read"}},
		run: func(_ context.Context, a *app.App, args Args) (any, error) {
			return importConfig(a, args.Str("path"))
		}})
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}

// decorate renders aliases with each rung's live/dead state, which is what makes the
// ladder readable rather than a list of opaque ids.
func decorate(a *app.App, as []store.Alias) []map[string]any {
	out := make([]map[string]any, 0, len(as))
	for _, al := range as {
		rungs := make([]map[string]any, 0, len(al.Rungs))
		for _, r := range al.Rungs {
			entry := map[string]any{"source_id": r.SourceID, "model": r.ModelID}
			s, err := a.DB.Source(r.SourceID)
			if err != nil {
				entry["source"], entry["available"] = r.SourceID, false
				entry["reason"] = i18n.T("source.notFound", r.SourceID)
			} else {
				entry["source"], entry["where"] = s.Name, s.Kind
				entry["available"] = s.Status == store.StatusLive
				if s.Status != store.StatusLive {
					entry["reason"] = i18n.T("alias.rungUnavailable", len(rungs)+1,
						s.Name+"/"+r.ModelID, i18n.SourceStatus(s.Status))
				}
			}
			rungs = append(rungs, entry)
		}
		out = append(out, map[string]any{"name": al.Name, "rungs": rungs,
			"updated_at": al.UpdatedAt})
	}
	return out
}

// ---- portable configuration (§4.2 closure) ----

type portable struct {
	Format    string         `json:"format"`
	Version   int            `json:"version"`
	Written   string         `json:"written"`
	Sources   []store.Source `json:"sources"`
	Models    []store.Model  `json:"models"`
	Aliases   []store.Alias  `json:"aliases"`
	Remaps    []store.Remap  `json:"remaps"`
	Grants    []store.Grant  `json:"grants"`
	VaultBlob []byte         `json:"vault_blob"`
}

func exportConfig(a *app.App, path string) (any, error) {
	if path == "" {
		path = filepath.Join(a.Dir, "ferrule-config.json")
	}
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
	remaps, err := a.DB.Remaps()
	if err != nil {
		return nil, err
	}
	grants, err := a.DB.Grants()
	if err != nil {
		return nil, err
	}
	blob, err := a.Vault.Blob()
	if err != nil {
		return nil, err
	}
	p := portable{
		Format: "ferrule-config", Version: 1, Written: time.Now().Format(time.RFC3339),
		Sources: srcs, Models: models, Aliases: aliases, Remaps: remaps, Grants: grants,
		VaultBlob: blob,
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	// 0600: the file carries the encrypted key blob, and the identity that opens it may
	// well be sitting next to it on the same machine.
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "bytes": len(raw), "sources": len(srcs),
		"aliases": len(aliases), "grants": len(grants)}, nil
}

func importConfig(a *app.App, path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p portable
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Format != "ferrule-config" {
		return nil, fmt.Errorf("%s is not a Ferrule configuration file", path)
	}
	if len(p.VaultBlob) > 0 {
		if err := a.Vault.SetBlob(p.VaultBlob); err != nil {
			return nil, err
		}
	}
	for _, s := range p.Sources {
		if err := a.DB.PutSource(s); err != nil {
			return nil, err
		}
	}
	bySource := map[string][]store.Model{}
	for _, m := range p.Models {
		bySource[m.SourceID] = append(bySource[m.SourceID], m)
	}
	for sid, ms := range bySource {
		if err := a.DB.ReplaceModels(sid, ms); err != nil {
			return nil, err
		}
	}
	for _, al := range p.Aliases {
		if err := a.DB.PutAlias(al); err != nil {
			return nil, err
		}
	}
	for _, r := range p.Remaps {
		if err := a.DB.PutRemap(r); err != nil {
			return nil, err
		}
	}
	return map[string]any{"path": path, "sources": len(p.Sources),
		"aliases": len(p.Aliases), "grants": len(p.Grants)}, nil
}
