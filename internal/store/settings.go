package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

// Setting reads a setting, returning def when unset.
func (d *DB) Setting(k, def string) string {
	var v string
	err := d.sql.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	if err != nil {
		return def
	}
	return v
}

// SetSetting writes a setting.
func (d *DB) SetSetting(k, v string) error {
	_, err := d.sql.Exec(`INSERT INTO settings (k,v) VALUES (?,?)
ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// Setting keys.
const (
	SetContentLogging = "content_logging" // "on" | "off" (default off, §4.5)
	SetCrossOrigin    = "cross_origin"    // "on" | "off" (default off, developer setting)
)

// StagedOp is a mutating control-plane op waiting for a person to apply it (§4.7).
type StagedOp struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	Payload   string `json:"payload"`
	Door      string `json:"door"`
	Caller    string `json:"caller"`
	CreatedAt int64  `json:"created_at"`
	AppliedAt int64  `json:"applied_at"`
}

// Stage records a mutating op without landing it.
func (d *DB) Stage(op, payload, door, caller string) (StagedOp, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return StagedOp{}, err
	}
	s := StagedOp{ID: hex.EncodeToString(b), Op: op, Payload: payload, Door: door,
		Caller: caller, CreatedAt: now()}
	_, err := d.sql.Exec(`INSERT INTO staged_ops (id,op,payload,door,caller,created_at) VALUES (?,?,?,?,?,?)`,
		s.ID, s.Op, s.Payload, s.Door, s.Caller, s.CreatedAt)
	return s, err
}

// StagedOps lists ops still awaiting a person.
func (d *DB) StagedOps() ([]StagedOp, error) {
	rows, err := d.sql.Query(`SELECT id,op,payload,door,caller,created_at,applied_at
FROM staged_ops WHERE applied_at = 0 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StagedOp
	for rows.Next() {
		var s StagedOp
		if err := rows.Scan(&s.ID, &s.Op, &s.Payload, &s.Door, &s.Caller, &s.CreatedAt, &s.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StagedOp fetches one staged op.
func (d *DB) StagedOp(id string) (StagedOp, error) {
	var s StagedOp
	err := d.sql.QueryRow(`SELECT id,op,payload,door,caller,created_at,applied_at FROM staged_ops WHERE id=?`, id).
		Scan(&s.ID, &s.Op, &s.Payload, &s.Door, &s.Caller, &s.CreatedAt, &s.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StagedOp{}, ErrNotFound
	}
	return s, err
}

// MarkApplied stamps a staged op as landed.
func (d *DB) MarkApplied(id string) error {
	_, err := d.sql.Exec(`UPDATE staged_ops SET applied_at=? WHERE id=?`, now(), id)
	return err
}

// DiscardStaged drops a staged op without applying it.
func (d *DB) DiscardStaged(id string) error {
	_, err := d.sql.Exec(`DELETE FROM staged_ops WHERE id=?`, id)
	return err
}

// ControlCall is one recorded control-plane call, with its door and caller (§4.7).
type ControlCall struct {
	TS      int64  `json:"ts"`
	Op      string `json:"op"`
	Door    string `json:"door"`
	Caller  string `json:"caller"`
	Outcome string `json:"outcome"`
}

// RecordControl logs a control-plane call.
func (d *DB) RecordControl(op, door, caller, outcome string) error {
	_, err := d.sql.Exec(`INSERT INTO control_log (ts,op,door,caller,outcome) VALUES (?,?,?,?,?)`,
		now(), op, door, caller, outcome)
	return err
}

// ControlLog returns recent control-plane calls, newest first.
func (d *DB) ControlLog(limit int) ([]ControlCall, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.Query(`SELECT ts,op,door,caller,outcome FROM control_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ControlCall
	for rows.Next() {
		var c ControlCall
		if err := rows.Scan(&c.TS, &c.Op, &c.Door, &c.Caller, &c.Outcome); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
