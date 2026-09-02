// Package vault is the encrypted home for provider keys.
//
// Threat model, stated plainly (§4.5): the vault protects keys at rest from anything
// that reads the disk — backups, sync clients, other user accounts, a stolen laptop with
// FileVault off, and the casual `grep -r sk- ~` that finds keys in a dozen .env files.
// It does not protect against code already running as this user with the vault unlocked;
// no local single-user secret store can, because the daemon must be able to read the key
// to make the request. Passphrase mode narrows that window: no unlock material touches
// the disk at all.
package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"filippo.io/age"

	"ferrule/internal/i18n"
)

// ErrNotFound is returned when no secret is stored under a ref.
var ErrNotFound = errors.New("vault: no secret for ref")

// Vault is the key-custody surface. Implementations must never write plaintext.
type Vault interface {
	// Backend names the storage backend for display (already localized).
	Backend() string
	Put(ref, secret string) error
	Get(ref string) (string, error)
	Delete(ref string) error
	Refs() ([]string, error)
	// Blob returns the encrypted store bytes for portable export (§4.2 closure).
	Blob() ([]byte, error)
	// SetBlob replaces the store from an exported blob, merging refs.
	SetBlob(b []byte) error
}

type ageVault struct {
	mu       sync.RWMutex
	path     string
	ident    age.Identity
	recip    age.Recipient
	scrypted bool
	cache    map[string]string
	loaded   bool
}

// Open returns the vault living in dir. When passphrase is non-empty the store is
// scrypt-sealed and no unlock material is written to disk; otherwise an X25519 identity
// file is created at 0600 alongside the store.
func Open(dir, passphrase string) (Vault, error) {
	v := &ageVault{
		path:  filepath.Join(dir, "vault.age"),
		cache: map[string]string{},
	}
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return nil, err
		}
		// Deliberately low-ish work factor: the daemon unlocks on every start.
		r.SetWorkFactor(18)
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, err
		}
		v.recip, v.ident, v.scrypted = r, id, true
	} else {
		id, err := loadOrCreateIdentity(filepath.Join(dir, "vault.identity"))
		if err != nil {
			return nil, err
		}
		v.ident, v.recip = id, id.Recipient()
	}
	if err := v.load(); err != nil {
		return nil, err
	}
	return v, nil
}

func loadOrCreateIdentity(path string) (*age.X25519Identity, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	// 0600, written atomically so a crash never leaves a truncated identity.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return id, nil
}

func (v *ageVault) Backend() string {
	if v.scrypted {
		return i18n.T("vault.backend.age") + " · passphrase"
	}
	return i18n.T("vault.backend.age")
}

func (v *ageVault) load() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, err := os.ReadFile(v.path)
	if os.IsNotExist(err) {
		v.cache, v.loaded = map[string]string{}, true
		return nil
	}
	if err != nil {
		return err
	}
	m, err := v.decrypt(raw)
	if err != nil {
		return err
	}
	v.cache, v.loaded = m, true
	return nil
}

func (v *ageVault) decrypt(raw []byte) (map[string]string, error) {
	r, err := age.Decrypt(bytes.NewReader(raw), v.ident)
	if err != nil {
		if v.scrypted {
			return nil, fmt.Errorf("%s: %w", i18n.T("vault.badPassphrase"), err)
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("vault.locked"), err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(bytes.TrimSpace(plain)) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// flush must be called with the write lock held.
func (v *ageVault) flush() error {
	plain, err := json.Marshal(v.cache)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.recip)
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}

func (v *ageVault) Put(ref, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New(i18n.T("vault.plaintextRefused"))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cache[ref] = secret
	return v.flush()
}

func (v *ageVault) Get(ref string) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	s, ok := v.cache[ref]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, i18n.T("vault.noKey", ref))
	}
	return s, nil
}

func (v *ageVault) Delete(ref string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.cache, ref)
	return v.flush()
}

func (v *ageVault) Refs() ([]string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.cache))
	for k := range v.cache {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (v *ageVault) Blob() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	raw, err := os.ReadFile(v.path)
	if os.IsNotExist(err) {
		return []byte{}, nil
	}
	return raw, err
}

func (v *ageVault) SetBlob(b []byte) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	m, err := v.decrypt(b)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, val := range m {
		v.cache[k] = val
	}
	return v.flush()
}

// Ref builds the vault ref for a source id. Refs are opaque handles; only the ref ever
// reaches SQLite, never the secret.
func Ref(sourceID string) string { return "source:" + sourceID }
