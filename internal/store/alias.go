package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Rung is one step of an alias's fallback ladder.
type Rung struct {
	SourceID string `json:"source_id"`
	ModelID  string `json:"model_id"`
}

// Alias is a first-class routing object: a name resolving to an ordered ladder.
type Alias struct {
	Name      string `json:"name"`
	Rungs     []Rung `json:"rungs"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// PutAlias creates or replaces an alias and its ladder.
func (d *DB) PutAlias(a Alias) error {
	if len(a.Rungs) == 0 {
		return errors.New("alias: empty ladder")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO aliases (name,created_at,updated_at) VALUES (?,?,?)
ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`, a.Name, now(), now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM alias_rungs WHERE alias=?`, a.Name); err != nil {
		return err
	}
	for i, r := range a.Rungs {
		if _, err := tx.Exec(`INSERT INTO alias_rungs (alias,position,source_id,model_id) VALUES (?,?,?,?)`,
			a.Name, i, r.SourceID, r.ModelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Alias fetches one alias with its ladder in order.
func (d *DB) Alias(name string) (Alias, error) {
	a := Alias{Name: name}
	err := d.sql.QueryRow(`SELECT created_at,updated_at FROM aliases WHERE name=?`, name).
		Scan(&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Alias{}, ErrNotFound
	}
	if err != nil {
		return Alias{}, err
	}
	rows, err := d.sql.Query(`SELECT source_id,model_id FROM alias_rungs WHERE alias=? ORDER BY position`, name)
	if err != nil {
		return Alias{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Rung
		if err := rows.Scan(&r.SourceID, &r.ModelID); err != nil {
			return Alias{}, err
		}
		a.Rungs = append(a.Rungs, r)
	}
	return a, rows.Err()
}

// Aliases lists every alias with its ladder.
func (d *DB) Aliases() ([]Alias, error) {
	rows, err := d.sql.Query(`SELECT name FROM aliases ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	out := make([]Alias, 0, len(names))
	for _, n := range names {
		a, err := d.Alias(n)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// DeleteAlias removes an alias and its ladder.
func (d *DB) DeleteAlias(name string) error {
	_, err := d.sql.Exec(`DELETE FROM aliases WHERE name=?`, name)
	return err
}

// Remap intercepts a hardcoded model id an app insists on sending.
type Remap struct {
	FromModel string `json:"from_model"`
	Target    string `json:"target"` // alias name, or "source_id/model_id"
	CreatedAt int64  `json:"created_at"`
}

// PutRemap creates or replaces a remap.
func (d *DB) PutRemap(r Remap) error {
	_, err := d.sql.Exec(`
INSERT INTO remaps (from_model,target,created_at) VALUES (?,?,?)
ON CONFLICT(from_model) DO UPDATE SET target=excluded.target`, r.FromModel, r.Target, now())
	return err
}

// Remap fetches one remap.
func (d *DB) Remap(from string) (Remap, error) {
	r := Remap{FromModel: from}
	err := d.sql.QueryRow(`SELECT target,created_at FROM remaps WHERE from_model=?`, from).
		Scan(&r.Target, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Remap{}, ErrNotFound
	}
	return r, err
}

// Remaps lists every remap.
func (d *DB) Remaps() ([]Remap, error) {
	rows, err := d.sql.Query(`SELECT from_model,target,created_at FROM remaps ORDER BY from_model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Remap
	for rows.Next() {
		var r Remap
		if err := rows.Scan(&r.FromModel, &r.Target, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRemap removes a remap.
func (d *DB) DeleteRemap(from string) error {
	_, err := d.sql.Exec(`DELETE FROM remaps WHERE from_model=?`, from)
	return err
}

// SplitTarget splits a "source_id/model_id" target. ok is false for a bare alias name.
func SplitTarget(t string) (sourceID, modelID string, ok bool) {
	i := strings.Index(t, "/")
	if i <= 0 || i == len(t)-1 {
		return "", "", false
	}
	return t[:i], t[i+1:], true
}
