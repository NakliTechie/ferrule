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
	return &App{
		Dir: dir, DB: db, Vault: v, Catalog: cat,
		Discovery: discovery.New(db, v, cat),
	}, nil
}

// Close releases the database handle.
func (a *App) Close() error { return a.DB.Close() }
