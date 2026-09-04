// Package config resolves Ferrule's on-disk home. XDG-respecting, overridable.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultPort is the daemon's listening port. Overridable by flag and FERRULE_PORT.
const DefaultPort = 8899

// Dir returns the configuration directory, creating it at 0700 if absent and tightening
// it if it is not.
//
// MkdirAll does nothing to a directory that already exists, so the 0700 applied only to
// fresh installs. A directory the person made themselves, or restored from a backup, or
// unpacked from an archive, kept whatever mode it arrived with — and this is the
// directory holding the vault. Tightening is only ever a narrowing, so doing it on every
// start is safe and is the only way an existing install gets fixed.
func Dir() (string, error) {
	dir := resolve()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
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
