package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// TokenPrefix marks a Ferrule app token so it is never confused with a provider key.
const TokenPrefix = "frl_"

// Grant is a per-app token: the app's identity to Ferrule, not a provider key.
type Grant struct {
	ID        string `json:"id"`
	App       string `json:"app"`
	CreatedAt int64  `json:"created_at"`
	RevokedAt int64  `json:"revoked_at"`
}

// Revoked reports whether the grant has been revoked.
func (g Grant) Revoked() bool { return g.RevokedAt != 0 }

// HashToken is the one-way function stored in SQLite. The token itself is never stored.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// MintGrant creates a token for app and returns the grant plus the token, shown once.
func (d *DB) MintGrant(app string) (Grant, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Grant{}, "", err
	}
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	idb := make([]byte, 6)
	if _, err := rand.Read(idb); err != nil {
		return Grant{}, "", err
	}
	g := Grant{ID: hex.EncodeToString(idb), App: app, CreatedAt: now()}
	_, err := d.sql.Exec(`INSERT INTO grants (id,app,token_hash,created_at) VALUES (?,?,?,?)`,
		g.ID, g.App, HashToken(tok), g.CreatedAt)
	if err != nil {
		return Grant{}, "", err
	}
	return g, tok, nil
}

// GrantByToken resolves a presented token. A revoked grant is returned with Revoked set
// so the caller can answer 401 rather than 404.
func (d *DB) GrantByToken(tok string) (Grant, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return Grant{}, ErrNotFound
	}
	h := HashToken(tok)
	rows, err := d.sql.Query(`SELECT id,app,token_hash,created_at,revoked_at FROM grants`)
	if err != nil {
		return Grant{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Grant
		var stored string
		if err := rows.Scan(&g.ID, &g.App, &stored, &g.CreatedAt, &g.RevokedAt); err != nil {
			return Grant{}, err
		}
		// Constant-time compare so a token is never distinguished by timing.
		if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
			return g, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Grant{}, err
	}
	return Grant{}, ErrNotFound
}

// Grants lists every grant, revoked ones included.
func (d *DB) Grants() ([]Grant, error) {
	rows, err := d.sql.Query(`SELECT id,app,created_at,revoked_at FROM grants ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.App, &g.CreatedAt, &g.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Grant fetches one grant by id.
func (d *DB) Grant(id string) (Grant, error) {
	var g Grant
	err := d.sql.QueryRow(`SELECT id,app,created_at,revoked_at FROM grants WHERE id=?`, id).
		Scan(&g.ID, &g.App, &g.CreatedAt, &g.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	return g, err
}

// RevokeGrant marks a grant revoked. The row is kept so its ledger history stays readable.
func (d *DB) RevokeGrant(id string) error {
	res, err := d.sql.Exec(`UPDATE grants SET revoked_at=? WHERE id=? AND revoked_at=0`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := d.Grant(id); err != nil {
			return ErrNotFound
		}
	}
	return nil
}
