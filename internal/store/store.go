// Package store is Ferrule's local persistence: SQLite in the config dir, nothing else.
// No Postgres, no server database (§4.8). Provider keys never reach this package —
// only their opaque vault refs.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned for a missing row.
var ErrNotFound = errors.New("store: not found")

// Kind and lane constants.
const (
	KindLocal = "local"
	KindCloud = "cloud"

	LaneTokens      = "tokens"
	LanePassthrough = "passthrough"

	StatusProbing = "probing"
	StatusLive    = "live"
	StatusFailed  = "failed"

	EgressLocal = "local"
	EgressCloud = "cloud"
)

// DB wraps the SQLite handle.
type DB struct{ sql *sql.DB }

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*DB, error) {
	h, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(1)
	if _, err := h.Exec(schema); err != nil {
		h.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	d := &DB{sql: h}
	if err := d.migrate(); err != nil {
		h.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// added lists every column introduced after a table first shipped. CREATE TABLE IF NOT
// EXISTS is silent about an existing table that is missing a column, and the failure
// surfaces later as a raw SQL error from somewhere unrelated — so the columns are
// declared here and added on open. Purely additive: nothing is renamed or dropped.
var added = []struct{ table, column, decl string }{
	{"sources", "status_code", `TEXT NOT NULL DEFAULT ''`},
	{"sources", "status_remedy", `TEXT NOT NULL DEFAULT ''`},
	{"sources", "insecure", `INTEGER NOT NULL DEFAULT 0`},
}

func (d *DB) migrate() error {
	for _, a := range added {
		has, err := d.hasColumn(a.table, a.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			a.table, a.column, a.decl)); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) hasColumn(table, column string) (bool, error) {
	rows, err := d.sql.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close releases the handle.
func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the handle for the few places that need raw access (tests, aggregation).
func (d *DB) SQL() *sql.DB { return d.sql }

func now() int64 { return time.Now().UnixMilli() }

// ---------- sources ----------

// Source is a place models come from: a detected local runtime or a pasted cloud key.
type Source struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Kind         string `json:"kind"`
	Lane         string `json:"lane"`
	BaseURL      string `json:"base_url"`
	KeyRef       string `json:"key_ref"`
	Status       string `json:"status"`
	StatusCode   string `json:"status_code"`
	StatusReason string `json:"status_reason"`
	StatusRemedy string `json:"status_remedy"`
	Detected     bool   `json:"detected"`
	Insecure     bool   `json:"insecure"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// PutSource inserts or updates a source by id.
func (d *DB) PutSource(s Source) error {
	if s.CreatedAt == 0 {
		s.CreatedAt = now()
	}
	s.UpdatedAt = now()
	_, err := d.sql.Exec(`
INSERT INTO sources (id,name,provider,kind,lane,base_url,key_ref,status,status_code,status_reason,status_remedy,detected,insecure,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, provider=excluded.provider, kind=excluded.kind, lane=excluded.lane,
  base_url=excluded.base_url, key_ref=excluded.key_ref, status=excluded.status,
  status_code=excluded.status_code, status_reason=excluded.status_reason,
  status_remedy=excluded.status_remedy, detected=excluded.detected,
  insecure=excluded.insecure, updated_at=excluded.updated_at`,
		s.ID, s.Name, s.Provider, s.Kind, s.Lane, s.BaseURL, s.KeyRef, s.Status, s.StatusCode,
		s.StatusReason, s.StatusRemedy, boolInt(s.Detected), boolInt(s.Insecure),
		s.CreatedAt, s.UpdatedAt)
	return err
}

// SetSourceStatus records the outcome of a probe or test: the code an agent branches on,
// the message a person reads, and the remedy either can act on.
func (d *DB) SetSourceStatus(id, status, code, reason, remedy string) error {
	_, err := d.sql.Exec(`
UPDATE sources SET status=?, status_code=?, status_reason=?, status_remedy=?, updated_at=?
WHERE id=?`, status, code, reason, remedy, now(), id)
	return err
}

const sourceCols = `id,name,provider,kind,lane,base_url,key_ref,status,status_code,status_reason,status_remedy,detected,insecure,created_at,updated_at`

func scanSource(rows interface{ Scan(...any) error }) (Source, error) {
	var s Source
	var det, insec int
	err := rows.Scan(&s.ID, &s.Name, &s.Provider, &s.Kind, &s.Lane, &s.BaseURL, &s.KeyRef,
		&s.Status, &s.StatusCode, &s.StatusReason, &s.StatusRemedy, &det, &insec,
		&s.CreatedAt, &s.UpdatedAt)
	s.Detected, s.Insecure = det != 0, insec != 0
	return s, err
}

// Sources lists every source, newest first.
func (d *DB) Sources() ([]Source, error) {
	rows, err := d.sql.Query(`SELECT ` + sourceCols + ` FROM sources ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Source fetches one source by id.
func (d *DB) Source(id string) (Source, error) {
	row := d.sql.QueryRow(`SELECT `+sourceCols+` FROM sources WHERE id=?`, id)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return s, err
}

// SourceByName fetches one source by its display name.
func (d *DB) SourceByName(name string) (Source, error) {
	row := d.sql.QueryRow(`SELECT `+sourceCols+` FROM sources WHERE name=?`, name)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return s, err
}

// DeleteSource removes a source and its models.
func (d *DB) DeleteSource(id string) error {
	_, err := d.sql.Exec(`DELETE FROM sources WHERE id=?`, id)
	return err
}

// ---------- models ----------

// Model is one classified model belonging to a source.
type Model struct {
	SourceID      string   `json:"source_id"`
	ModelID       string   `json:"model_id"`
	DisplayName   string   `json:"display_name"`
	Capabilities  []string `json:"capabilities"`
	Modalities    []string `json:"modalities"`
	ContextLength int      `json:"context_length"`
	Async         bool     `json:"async"`
	InCost        float64  `json:"in_cost"`
	OutCost       float64  `json:"out_cost"`
	ClassifiedBy  string   `json:"classified_by"`
	UpdatedAt     int64    `json:"updated_at"`
}

// ReplaceModels swaps a source's whole model set atomically.
func (d *DB) ReplaceModels(sourceID string, ms []Model) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM models WHERE source_id=?`, sourceID); err != nil {
		return err
	}
	for _, m := range ms {
		caps, _ := json.Marshal(defaultSlice(m.Capabilities))
		mods, _ := json.Marshal(defaultSlice(m.Modalities))
		if _, err := tx.Exec(`
INSERT INTO models (source_id,model_id,display_name,capabilities,modalities,context_length,is_async,in_cost,out_cost,classified_by,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			sourceID, m.ModelID, m.DisplayName, string(caps), string(mods), m.ContextLength,
			boolInt(m.Async), m.InCost, m.OutCost, m.ClassifiedBy, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const modelCols = `source_id,model_id,display_name,capabilities,modalities,context_length,is_async,in_cost,out_cost,classified_by,updated_at`

func scanModel(rows interface{ Scan(...any) error }) (Model, error) {
	var m Model
	var caps, mods string
	var as int
	err := rows.Scan(&m.SourceID, &m.ModelID, &m.DisplayName, &caps, &mods, &m.ContextLength,
		&as, &m.InCost, &m.OutCost, &m.ClassifiedBy, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	m.Async = as != 0
	_ = json.Unmarshal([]byte(caps), &m.Capabilities)
	_ = json.Unmarshal([]byte(mods), &m.Modalities)
	return m, nil
}

// Models lists every model; pass a source id to scope it.
func (d *DB) Models(sourceID string) ([]Model, error) {
	q := `SELECT ` + modelCols + ` FROM models`
	var args []any
	if sourceID != "" {
		q += ` WHERE source_id=?`
		args = append(args, sourceID)
	}
	q += ` ORDER BY source_id, model_id`
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Model fetches one model.
func (d *DB) Model(sourceID, modelID string) (Model, error) {
	row := d.sql.QueryRow(`SELECT `+modelCols+` FROM models WHERE source_id=? AND model_id=?`, sourceID, modelID)
	m, err := scanModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	return m, err
}

// FindModel resolves a bare model id across live sources, preferring local.
func (d *DB) FindModel(modelID string) (Model, Source, error) {
	rows, err := d.sql.Query(`
SELECT `+prefixed("m", modelCols)+`, `+prefixed("s", sourceCols)+`
FROM models m JOIN sources s ON s.id = m.source_id
WHERE m.model_id = ? AND s.status = 'live'`, modelID)
	if err != nil {
		return Model{}, Source{}, err
	}
	defer rows.Close()
	type pair struct {
		m Model
		s Source
	}
	var found []pair
	for rows.Next() {
		var m Model
		var s Source
		var caps, mods string
		var as, det, insec int
		if err := rows.Scan(&m.SourceID, &m.ModelID, &m.DisplayName, &caps, &mods, &m.ContextLength,
			&as, &m.InCost, &m.OutCost, &m.ClassifiedBy, &m.UpdatedAt,
			&s.ID, &s.Name, &s.Provider, &s.Kind, &s.Lane, &s.BaseURL, &s.KeyRef, &s.Status,
			&s.StatusCode, &s.StatusReason, &s.StatusRemedy, &det, &insec, &s.CreatedAt,
			&s.UpdatedAt); err != nil {
			return Model{}, Source{}, err
		}
		m.Async, s.Detected, s.Insecure = as != 0, det != 0, insec != 0
		_ = json.Unmarshal([]byte(caps), &m.Capabilities)
		_ = json.Unmarshal([]byte(mods), &m.Modalities)
		found = append(found, pair{m, s})
	}
	if err := rows.Err(); err != nil {
		return Model{}, Source{}, err
	}
	if len(found) == 0 {
		return Model{}, Source{}, ErrNotFound
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].s.Kind == KindLocal && found[j].s.Kind != KindLocal
	})
	return found[0].m, found[0].s, nil
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i := range parts {
		parts[i] = p + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Learn records what a live probe discovered about a model the capability catalog had
// nothing to say about. The probe costs a real request against a real provider; keeping
// the answer means the next probe of that id is free, and the system quietly gets better
// the more it is used.
func (d *DB) Learn(provider, modelID string, caps []string) error {
	if provider == "" || modelID == "" || len(caps) == 0 {
		return nil
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`
INSERT INTO learned (provider, model_id, capabilities, learned_at) VALUES (?,?,?,?)
ON CONFLICT(provider, model_id) DO UPDATE SET
  capabilities=excluded.capabilities, learned_at=excluded.learned_at`,
		provider, modelID, string(b), now())
	return err
}

// Learned returns what a previous probe found for a model id.
func (d *DB) Learned(provider, modelID string) ([]string, bool) {
	var raw string
	if err := d.sql.QueryRow(`SELECT capabilities FROM learned WHERE provider=? AND model_id=?`,
		provider, modelID).Scan(&raw); err != nil {
		return nil, false
	}
	var caps []string
	if err := json.Unmarshal([]byte(raw), &caps); err != nil || len(caps) == 0 {
		return nil, false
	}
	return caps, true
}

// LearnedCount reports how many ids the probe cache holds.
func (d *DB) LearnedCount() int {
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM learned`).Scan(&n)
	return n
}
