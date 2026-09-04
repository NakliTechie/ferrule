// Package app wires Ferrule's parts together. The CLI, the daemon, and the tests are all
// clients of this one core — there is no second path (§4.8).
package app

import (
	"os"
	"path/filepath"

	"ferrule/internal/catalog"
	"ferrule/internal/config"
	"ferrule/internal/discovery"
	"ferrule/internal/store"
	"ferrule/internal/vault"
)

// App holds the opened core.
type App struct {
	Dir       string
	DB        *store.DB
	Vault     vault.Vault
	Catalog   *catalog.Catalog
	Discovery *discovery.Engine
}

// Options tune how the core opens. Zero values are the normal path.
type Options struct {
	// Dir overrides the config directory. Empty uses config.Dir().
	Dir string
	// Passphrase seals the vault with scrypt instead of an on-disk identity file.
	Passphrase string
}

// Open brings up the config dir, the database, the vault, and the catalog.
func Open(o Options) (*App, error) {
	dir := o.Dir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	pass := o.Passphrase
	if pass == "" {
		pass = os.Getenv("FERRULE_PASSPHRASE")
	}

	v, err := vault.Open(dir, pass)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(dir, config.DBFile))
	if err != nil {
		return nil, err
	}
	cat := catalog.Open(dir)
	a := &App{
		Dir: dir, DB: db, Vault: v, Catalog: cat,
		Discovery: discovery.New(db, v, cat),
	}
	// Reconcile the two stores on the way up. A key that no source refers to any more is
	// a secret nothing can reach and nobody can delete; the only place to notice one is
	// here, where both stores are open at once.
	if err := a.pruneOrphanKeys(); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) pruneOrphanKeys() error {
	srcs, err := a.DB.Sources()
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(srcs))
	for _, s := range srcs {
		if s.KeyRef != "" {
			live[s.KeyRef] = true
		}
	}
	// The household key is the one token Ferrule stores, so the sweep has to know about
	// it. Without this the reconciler would delete the shared key on every start and the
	// panel would show a key nothing accepts.
	grants, err := a.DB.Grants()
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.Shared && !g.Revoked() {
			live[vault.GrantRef(g.ID)] = true
		}
	}
	_, err = vault.Prune(a.Vault, live)
	return err
}

// HouseholdKey returns the one key the whole house uses, minting it the first time.
//
// It exists because per-person keys are the wrong default for the thing this is: a box in
// the house that everyone uses. Accountability per person is a real feature and it is an
// upgrade, not the price of admission.
func (a *App) HouseholdKey() (store.Grant, string, error) {
	grants, err := a.DB.Grants()
	if err != nil {
		return store.Grant{}, "", err
	}
	for _, g := range grants {
		if !g.Shared || g.Revoked() {
			continue
		}
		tok, err := a.Vault.Get(vault.GrantRef(g.ID))
		if err == nil {
			return g, tok, nil
		}
		// The row says there is a shared key and the vault cannot produce it. Nothing can
		// authenticate with a token nobody has, so the honest repair is to retire this
		// grant and mint one that works, rather than showing a key that is not the key.
		_ = a.DB.RevokeGrant(g.ID)
	}
	g, tok, err := a.DB.MintGrant(HouseholdApp, true)
	if err != nil {
		return store.Grant{}, "", err
	}
	// Vault first, then it is real. A grant row whose token cannot be read back is the
	// broken state this function just repaired.
	if err := a.Vault.Put(vault.GrantRef(g.ID), tok); err != nil {
		_ = a.DB.RevokeGrant(g.ID)
		return store.Grant{}, "", err
	}
	return g, tok, nil
}

// RegenerateHouseholdKey retires the current shared key and issues a new one. This is
// what "someone left, or it got out" looks like: every device using the old one stops
// working at once, which is the point.
func (a *App) RegenerateHouseholdKey() (store.Grant, string, error) {
	grants, err := a.DB.Grants()
	if err != nil {
		return store.Grant{}, "", err
	}
	for _, g := range grants {
		if g.Shared && !g.Revoked() {
			if err := a.DB.RevokeGrant(g.ID); err != nil {
				return store.Grant{}, "", err
			}
			// The old token has no use now and is one more secret on disk than needed.
			_ = a.Vault.Delete(vault.GrantRef(g.ID))
		}
	}
	return a.HouseholdKey()
}

// HouseholdApp is the name the shared key is recorded under, so the usage view says
// something a person recognises rather than an id.
const HouseholdApp = "household"

// Close releases the database handle.
func (a *App) Close() error { return a.DB.Close() }
