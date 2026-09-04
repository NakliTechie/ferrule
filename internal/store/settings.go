package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

// Setting reads a setting, returning def when unset or unreadable.
func (d *DB) Setting(k, def string) string {
	v, _ := d.SettingOK(k, def)
	return v
}

// SettingOK reads a setting and says whether the answer is trustworthy.
//
// "Not set yet" and "the database would not answer" are different facts, and Setting
// returns the default for both. For a setting that opens a network port that is the wrong
// way round: a failed read re-enabled LAN inference after the person had turned it off,
// while the code that called it carried a comment saying an unreadable setting falls
// closed. It did not. Callers that guard something ask this one.
func (d *DB) SettingOK(k, def string) (string, bool) {
	var v string
	err := d.sql.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return def, true // genuinely unset; the default is the answer
	case err != nil:
		return def, false
	}
	return v, true
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
	SetCatalogRefresh = "catalog_refresh" // "on" | "off" (default on, disclosed)
	// SetSharing is whether the rest of the network may use the inference endpoints.
	// Default on: this is a household appliance, and one that only the machine it runs
	// on can use is not one. The control surface is never covered by this — it answers
	// only to this machine whatever the setting says.
	SetSharing = "sharing" // "on" | "off" (default on)
	// SetShareAddress is which of this machine's addresses the panel and the CLI hand
	// out. Unset means "whichever Ferrule would pick"; a stored value that stops being
	// served is cleared rather than shown.
	SetShareAddress = "share_address"
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

// ClaimStaged atomically marks a staged op applied, returning false when someone else
// already had it. The check and the write are one statement on purpose: doing them
// separately is what lets two concurrent applies both decide they are first.
func (d *DB) ClaimStaged(id string) (bool, error) {
	res, err := d.sql.Exec(`UPDATE staged_ops SET applied_at=? WHERE id=? AND applied_at=0`, now(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseStaged hands a claim back when the operation it guarded failed, so a corrected
// retry is possible rather than the entry being stranded.
func (d *DB) ReleaseStaged(id string) error {
	_, err := d.sql.Exec(`UPDATE staged_ops SET applied_at=0 WHERE id=?`, id)
	return err
}
