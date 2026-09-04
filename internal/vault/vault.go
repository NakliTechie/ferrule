// Package vault is the encrypted home for provider keys.
//
// # Threat model, stated exactly
//
// There are two modes, and they defend against different things. Saying so precisely
// matters more here than anywhere else in the product, because a vault that is trusted
// past what it actually does is worse than a .env file that nobody trusts at all.
//
// Identity mode (the default, and what lets the daemon start unattended) writes an age
// identity to vault.identity at 0600 beside the encrypted store. It defends against:
//
//   - another user account on the machine (0600),
//   - `grep -r sk- ~`, an editor's project-wide search, a screen share, a pasted diff —
//     the everyday leaks that put keys in a dozen .env files in the first place,
//   - a process that reads the store without the identity beside it.
//
// It does NOT defend against anyone who copies the whole config directory. A backup, a
// cloud-drive sync, or a thief with an unencrypted disk gets both files, and two files
// is one decryption. If that is your threat, identity mode is not enough, and Ferrule
// says so here rather than letting the word "encrypted" imply otherwise.
//
// Passphrase mode (serve --passphrase, or FERRULE_PASSPHRASE) writes nothing that can
// open the store. A copy of the config directory is then genuinely useless without the
// passphrase, and the cost is that the daemon cannot start unattended.
//
// Neither mode defends against code already running as this user against a live daemon.
// No local single-user secret store can: the daemon has to be able to read the key in
// order to make the request. What Ferrule offers there is the ledger — every use of
// every key is recorded, so a key used behind your back is a key you can see was used.
//
// A configuration export is a third case and is sealed under its own passphrase, so the
// file that leaves this machine is not protected by anything left on it.
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
	"time"

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
	// Seal returns the key store re-encrypted under a passphrase, for a portable export
	// that is genuinely one file (§4.2 closure). Sealing under the local identity would
	// produce a file that only opens next to that identity — two artefacts, not one.
	Seal(passphrase string) ([]byte, error)
	// Unseal merges a sealed export into this store.
	Unseal(b []byte, passphrase string) error
}

type ageVault struct {
	mu       sync.RWMutex
	path     string
	lockPath string
	ident    age.Identity
	recip    age.Recipient
	scrypted bool
	cache    map[string]string
}

// Open returns the vault living in dir. When passphrase is non-empty the store is
// scrypt-sealed and no unlock material is written to disk; otherwise an X25519 identity
// file is created at 0600 alongside the store.
func Open(dir, passphrase string) (Vault, error) {
	v := &ageVault{
		path:     filepath.Join(dir, "vault.age"),
		lockPath: filepath.Join(dir, "vault.lock"),
		cache:    map[string]string{},
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

// Unattended reports whether a daemon can open this vault with nobody at the keyboard.
//
// Identity mode can: the key sits at 0600 beside the store. Passphrase mode deliberately
// cannot — that is the whole point of it — so registering Ferrule to start at login under
// a passphrase vault would produce a login item that fails every morning and a person who
// finds out when the house cannot reach the endpoint.
//
// A directory with no vault yet is unattended: the first start will make an identity.
func Unattended(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "vault.identity")); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "vault.age"))
	return os.IsNotExist(err)
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
		v.cache = map[string]string{}
		return nil
	}
	if err != nil {
		return err
	}
	m, err := v.decrypt(raw)
	if err != nil {
		return err
	}
	v.cache = m
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
	return v.mutate(func(m map[string]string) { m[ref] = secret })
}

// mutate applies a change under an exclusive cross-process lock, re-reading the store
// first so the write is against what is on disk right now.
//
// A process-local mutex is not enough here. The daemon and the CLI are separate
// processes against one file, and each held its whole store in memory from startup: the
// daemon loads {A}, the CLI adds B and writes {A,B}, then the daemon adds C and writes
// {A,C} from its stale cache — and B is gone, silently, along with whatever provider
// account it was for.
func (v *ageVault) mutate(apply func(map[string]string)) error {
	unlock, err := v.lock()
	if err != nil {
		return err
	}
	defer unlock()

	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.reload(); err != nil {
		return err
	}
	apply(v.cache)
	return v.flush()
}

// reload re-reads the store from disk. Callers hold both the file lock and the mutex.
func (v *ageVault) reload() error {
	raw, err := os.ReadFile(v.path)
	if os.IsNotExist(err) {
		if v.cache == nil {
			v.cache = map[string]string{}
		}
		return nil
	}
	if err != nil {
		return err
	}
	m, err := v.decrypt(raw)
	if err != nil {
		return err
	}
	v.cache = m
	return nil
}

// lock takes an exclusive advisory lock on the vault, waiting briefly for another
// process to finish. The returned function releases it. The platform-specific half lives
// in lock_unix.go and lock_windows.go.
func (v *ageVault) lock() (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(v.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := lockFile(f); err == nil {
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		_ = f.Close()
		if time.Now().After(deadline) {
			return nil, errors.New(i18n.T("vault.busy"))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (v *ageVault) Get(ref string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Re-read before answering: another process may have added or rotated this key since
	// this one started, and handing back a key that is no longer the stored one is how a
	// rotation appears to have silently failed.
	if err := v.reload(); err != nil {
		return "", err
	}
	s, ok := v.cache[ref]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, i18n.T("vault.noKey", ref))
	}
	return s, nil
}

func (v *ageVault) Delete(ref string) error {
	return v.mutate(func(m map[string]string) { delete(m, ref) })
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

func (v *ageVault) Seal(passphrase string) ([]byte, error) {
	if len(passphrase) < 8 {
		return nil, errors.New(i18n.T("export.needPassphrase"))
	}
	v.mu.Lock()
	if err := v.reload(); err != nil {
		v.mu.Unlock()
		return nil, err
	}
	plain, err := json.Marshal(v.cache)
	v.mu.Unlock()
	if err != nil {
		return nil, err
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	// Higher than the daemon's own unlock factor: this file may sit in a backup or a
	// cloud drive for years, where an offline attacker has all the time they want.
	r.SetWorkFactor(20)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (v *ageVault) Unseal(b []byte, passphrase string) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	rd, err := age.Decrypt(bytes.NewReader(b), id)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("vault.badPassphrase"), err)
	}
	plain, err := io.ReadAll(rd)
	if err != nil {
		return err
	}
	m := map[string]string{}
	if err := json.Unmarshal(plain, &m); err != nil {
		return err
	}
	return v.mutate(func(cache map[string]string) {
		for k, val := range m {
			cache[k] = val
		}
	})
}

// Ref builds the vault ref for a source id. Refs are opaque handles; only the ref ever
// reaches SQLite, never the secret.
func Ref(sourceID string) string { return "source:" + sourceID }

// GrantRef is where a shared grant's own token lives. Ferrule keeps exactly one class of
// token it issued: the household's, because a key several people need over several days
// cannot be a key shown once. Per-person tokens are never stored.
func GrantRef(grantID string) string { return "grant:" + grantID }

// Prune removes stored secrets that nothing refers to any more.
//
// The vault and the source table are two stores with no transaction between them, so a
// crash or a failed write at the wrong moment can leave an encrypted blob that no source
// points at. That blob is unreachable through every surface — it cannot be listed, used,
// or deleted by a person — which makes it exactly the kind of secret that outlives the
// account it belonged to. Reconciling at startup is what stops "unreachable" from
// meaning "permanent".
func Prune(v Vault, live map[string]bool) (int, error) {
	refs, err := v.Refs()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ref := range refs {
		if live[ref] {
			continue
		}
		if err := v.Delete(ref); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
