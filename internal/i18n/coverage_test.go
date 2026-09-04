package i18n_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/NakliTechie/ferrule/internal/i18n"
)

// Strings are externalized from the first commit (FERRULE.md preamble). This is the
// check that keeps it true: every key the Go and the panel ask for must exist in the
// table, and every key in the table must be asked for somewhere.
func TestEveryRequestedStringKeyExists(t *testing.T) {
	root := repoRoot(t)
	table := map[string]bool{}
	for _, k := range i18n.Keys() {
		table[k] = true
	}
	if len(table) == 0 {
		t.Fatal("the string table is empty")
	}

	// Both the qualified call from other packages and the bare call inside this one.
	goKey := regexp.MustCompile(`\b(?:i18n\.)?T\(\s*"([a-z][a-zA-Z0-9_]*(?:[.-][a-zA-Z0-9_]+)+)"`)
	jsKey := regexp.MustCompile(`\bT\(\s*"([^"]+)"`)
	jsPair := regexp.MustCompile(`\[\s*"[a-zA-Z]*",\s*"((?:ui|app|source|alias|usage)\.[^"]+)"`)
	// COLS entries name their heading by key; only dotted keys count.
	jsProp := regexp.MustCompile(`\bkey:\s*"([a-z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)+)"`)

	used := map[string][]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "plan" {
				return filepath.SkipDir
			}
			return nil
		}
		var res []*regexp.Regexp
		switch filepath.Ext(path) {
		case ".go":
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			res = []*regexp.Regexp{goKey}
		case ".js":
			res = []*regexp.Regexp{jsKey, jsPair, jsProp}
		default:
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				used[m[1]] = append(used[m[1]], path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for k, where := range used {
		if !table[k] {
			missing = append(missing, k+" ("+where[0]+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d key(s) requested but not in the table:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	var orphan []string
	for k := range table {
		if len(used[k]) == 0 {
			orphan = append(orphan, k)
		}
	}
	sort.Strings(orphan)
	if len(orphan) > 0 {
		t.Errorf("%d key(s) in the table that nothing asks for:\n  %s",
			len(orphan), strings.Join(orphan, "\n  "))
	}
}

// A missing key must be loud, not silently blank — a gap in the copy should be visible
// in the surface, not invisible.
func TestMissingKeyIsLoud(t *testing.T) {
	got := i18n.T("no.such.key.exists")
	if !strings.Contains(got, "no.such.key.exists") {
		t.Errorf("T on a missing key returned %q; it must name the key", got)
	}
}

func TestBundledLocalesParse(t *testing.T) {
	if err := i18n.LoadError(); err != nil {
		t.Fatal(err)
	}
	if len(i18n.Locales()) == 0 {
		t.Fatal("no bundled locales")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}

// The string table is written for Go's fmt and rendered by two formatters: Go's, and the
// twelve lines of it in the panel. A verb the panel's formatter does not match is not a
// visible error — it silently shifts every later argument by one and drops the last. The
// panel did not know %q, and the dialog that hands a family member their key told them to
// point their app at "http:///v1".
func TestThePanelUnderstandsEveryVerbInTheTable(t *testing.T) {
	root := repoRoot(t)
	js, err := os.ReadFile(filepath.Join(root, "internal", "ui", "assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	// The verb matcher out of the panel's own T(), so this tests what ships rather than
	// a copy of it that can go stale.
	m := regexp.MustCompile(`s\.replace\(/([^/]+)/g`).FindSubmatch(js)
	if m == nil {
		t.Fatal("cannot find the panel's format-verb matcher in app.js; this test is broken")
	}
	panelVerbs, err := regexp.Compile(string(m[1]))
	if err != nil {
		t.Fatalf("the panel's matcher %q does not compile as a regexp: %v", m[1], err)
	}

	verb := regexp.MustCompile(`%[-#+ 0-9.]*[a-zA-Z]`)
	seen := map[string][]string{}
	for _, k := range i18n.Keys() {
		for _, v := range verb.FindAllString(i18n.Raw(k), -1) {
			seen[v] = append(seen[v], k)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no format verbs found in the table; the extractor is broken")
	}
	for v, keys := range seen {
		if !panelVerbs.MatchString(v) {
			sort.Strings(keys)
			t.Errorf("the table uses %s (%d keys, e.g. %s) and the panel's formatter does "+
				"not match it; every later argument in those strings shifts by one",
				v, len(keys), keys[0])
		}
	}
}
