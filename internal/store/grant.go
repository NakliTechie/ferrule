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
	// Shared marks the one key the whole household uses. Its token is kept in the vault
	// so it can be read out again — a key five people need over a week cannot be a key
	// shown once. A per-person key is not shared and is never stored: it exists as a hash
	// and nothing else, which is what makes "turn this one off" mean something.
	Shared bool `json:"shared,omitempty"`
}

// Revoked reports whether the grant has been revoked.
func (g Grant) Revoked() bool { return g.RevokedAt != 0 }

// HashToken is the one-way function stored in SQLite. The token itself is never stored.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// MintGrant creates a token for app and returns the grant plus the token. A per-person
// grant is shown once and never stored; a shared one is written to the vault by the
// caller, which is the only place a Ferrule token is ever kept.
func (d *DB) MintGrant(app string, shared bool) (Grant, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Grant{}, "", err
	}
	tok := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	idb := make([]byte, 6)
	if _, err := rand.Read(idb); err != nil {
		return Grant{}, "", err
	}
	g := Grant{ID: hex.EncodeToString(idb), App: app, CreatedAt: now(), Shared: shared}
	_, err := d.sql.Exec(`INSERT INTO grants (id,app,token_hash,created_at,shared) VALUES (?,?,?,?,?)`,
		g.ID, g.App, HashToken(tok), g.CreatedAt, boolInt(shared))
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
	rows, err := d.sql.Query(`SELECT id,app,token_hash,created_at,revoked_at,shared FROM grants`)
	if err != nil {
		return Grant{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Grant
		var stored string
		var shared int
		if err := rows.Scan(&g.ID, &g.App, &stored, &g.CreatedAt, &g.RevokedAt, &shared); err != nil {
			return Grant{}, err
		}
		g.Shared = shared != 0
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
	rows, err := d.sql.Query(`SELECT id,app,created_at,revoked_at,shared FROM grants ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		var shared int
		if err := rows.Scan(&g.ID, &g.App, &g.CreatedAt, &g.RevokedAt, &shared); err != nil {
			return nil, err
		}
		g.Shared = shared != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// Grant fetches one grant by id.
func (d *DB) Grant(id string) (Grant, error) {
	var g Grant
	var shared int
	err := d.sql.QueryRow(`SELECT id,app,created_at,revoked_at,shared FROM grants WHERE id=?`, id).
		Scan(&g.ID, &g.App, &g.CreatedAt, &g.RevokedAt, &shared)
	g.Shared = shared != 0
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

// PortableGrant is a grant plus the hash that recognises its token. It exists only for
// the configuration export (§4.2 closure): a person who moves their configuration to
// another machine expects their apps to keep working, and that needs the verifier to
// travel. The hash is one-way — carrying it does not carry the token.
type PortableGrant struct {
	Grant
	TokenHash string `json:"token_hash"`
}

// ExportGrants returns every grant with its token hash.
func (d *DB) ExportGrants() ([]PortableGrant, error) {
	rows, err := d.sql.Query(`SELECT id,app,token_hash,created_at,revoked_at,shared FROM grants ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortableGrant
	for rows.Next() {
		var g PortableGrant
		var shared int
		if err := rows.Scan(&g.ID, &g.App, &g.TokenHash, &g.CreatedAt, &g.RevokedAt, &shared); err != nil {
			return nil, err
		}
		g.Shared = shared != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// ImportGrants restores grants from an export, leaving any that already exist alone.
func (d *DB) ImportGrants(gs []PortableGrant) error {
	for _, g := range gs {
		if g.ID == "" || g.TokenHash == "" {
			continue
		}
		// `shared` has to survive the round trip, and not because of a flag in a list.
		// The household token lives in the vault under this grant's id, and the startup
		// sweep keeps a vault entry only when a live shared grant points at it — so an
		// import that lost the flag left the imported key in the vault until the next
		// start, and then deleted it. The configuration looked complete and stopped
		// working on the first restart.
		if _, err := d.sql.Exec(`
INSERT INTO grants (id,app,token_hash,created_at,revoked_at,shared) VALUES (?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET app=excluded.app,
  -- Revocation only ever moves one way. An export taken before a token was revoked
  -- carries revoked_at=0, and letting that overwrite a later revocation brings a dead
  -- credential back to life — including a household key that was rotated precisely
  -- because it got out.
  revoked_at=CASE WHEN grants.revoked_at != 0 THEN grants.revoked_at ELSE excluded.revoked_at END,
  shared=excluded.shared`,
			g.ID, g.App, g.TokenHash, g.CreatedAt, g.RevokedAt, boolInt(g.Shared)); err != nil {
			return err
		}
	}
	return nil
}
