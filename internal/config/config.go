// Package config resolves Ferrule's on-disk home. XDG-respecting, overridable.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultPort is the daemon's listening port. Overridable by flag and FERRULE_PORT.
const DefaultPort = 8899

// Dir returns the configuration directory, creating it at 0700 if absent.
func Dir() (string, error) {
	dir := resolve()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func resolve() string {
	if v := os.Getenv("FERRULE_CONFIG_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "ferrule")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ferrule"
	}
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("AppData"); v != "" {
			return filepath.Join(v, "ferrule")
		}
	}
	return filepath.Join(home, ".config", "ferrule")
}

// Path joins name onto the config directory.
func Path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Filenames, pinned on first write (§4.2).
const (
	DBFile        = "ferrule.db"
	VaultFile     = "vault.age"
	IdentityFile  = "vault.identity"
	CatalogFile   = "catalog.json"
	ExportDefault = "ferrule-config.json"
)
