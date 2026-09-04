// Package i18n holds every user-facing string in Ferrule. No user-facing copy is
// hardcoded at a call site; callers ask for a key and get the active locale's text.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

//go:embed locales/*.json
var locales embed.FS

const defaultLocale = "en"

var (
	mu      sync.RWMutex
	active  = defaultLocale
	tables  = map[string]map[string]string{}
	loadErr error
	once    sync.Once
)

func load() {
	entries, err := locales.ReadDir("locales")
	if err != nil {
		loadErr = err
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := locales.ReadFile("locales/" + e.Name())
		if err != nil {
			loadErr = err
			return
		}
		var table map[string]string
		if err := json.Unmarshal(raw, &table); err != nil {
			loadErr = fmt.Errorf("locale %s: %w", e.Name(), err)
			return
		}
		tables[strings.TrimSuffix(e.Name(), ".json")] = table
	}
	if env := os.Getenv("FERRULE_LOCALE"); env != "" {
		if _, ok := tables[env]; ok {
			active = env
		}
	}
}

// SetLocale switches the active locale. Unknown locales are ignored.
func SetLocale(name string) bool {
	once.Do(load)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := tables[name]; !ok {
		return false
	}
	active = name
	return true
}

// Locales lists the bundled locale codes.
func Locales() []string {
	once.Do(load)
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(tables))
	for k := range tables {
		out = append(out, k)
	}
	return out
}

// T returns the string for key in the active locale, formatted with args.
// A missing key returns the key itself wrapped in ⟨⟩ so gaps are loud, not silent.
func T(key string, args ...any) string {
	once.Do(load)
	mu.RLock()
	tbl, ok := tables[active]
	fallback := tables[defaultLocale]
	mu.RUnlock()
	var s string
	var found bool
	if ok {
		s, found = tbl[key]
	}
	if !found && fallback != nil {
		s, found = fallback[key]
	}
	if !found {
		return "⟨" + key + "⟩"
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Keys returns every key in the default locale. Used by the string-coverage test.
func Keys() []string {
	once.Do(load)
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(tables[defaultLocale]))
	for k := range tables[defaultLocale] {
		out = append(out, k)
	}
	return out
}

// Raw returns a key's unformatted string in the default locale, so a test can inspect
// the format verbs a string actually carries.
func Raw(key string) string {
	once.Do(load)
	mu.RLock()
	defer mu.RUnlock()
	return tables[defaultLocale][key]
}

// LoadError surfaces a malformed bundled locale at startup.
func LoadError() error { once.Do(load); return loadErr }

// SourceStatus returns the localized label for a source status. Statuses are computed,
// so this exists to keep every status key statically referenced and lintable rather than
// assembled from a string prefix at the call site.
func SourceStatus(status string) string {
	switch status {
	case "live":
		return T("source.status.live")
	case "failed":
		return T("source.status.failed")
	case "probing":
		return T("source.status.probing")
	default:
		return status
	}
}
