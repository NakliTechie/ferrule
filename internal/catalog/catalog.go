// Package catalog answers "what can this model do, and what does it cost?" by id.
//
// Capabilities and prices are never hardcoded at a call site: they are looked up in a
// dated snapshot that is refreshed in the background from a maintained remote source and
// cached on disk. Render is cache-first — a cold or offline start still classifies from
// the bundled snapshot rather than blocking.
package catalog

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed data/snapshot.json
var bundled []byte

// RemoteURL is the maintained source refreshed in the background.
const RemoteURL = "https://models.dev/api.json"

// RefreshAfter is how stale the cache may get before a background refresh fires.
const RefreshAfter = 24 * time.Hour

// Entry is one catalog record. Match is a case-insensitive substring of a model id;
// the longest match wins so specific ids beat family prefixes.
type Entry struct {
	Match        string   `json:"match"`
	Capabilities []string `json:"capabilities"`
	Modalities   []string `json:"modalities,omitempty"`
	Context      int      `json:"context"`
	InCost       float64  `json:"in_cost"`
	OutCost      float64  `json:"out_cost"`
	Async        bool     `json:"async,omitempty"`
}

type snapshot struct {
	SnapshotDate string  `json:"snapshot_date"`
	Source       string  `json:"source"`
	Note         string  `json:"note,omitempty"`
	FetchedAt    int64   `json:"fetched_at,omitempty"`
	Entries      []Entry `json:"entries"`
}

// Catalog is the lookup surface.
type Catalog struct {
	mu        sync.RWMutex
	entries   []Entry
	date      string
	fetchedAt time.Time
	path      string
	client    *http.Client
	remote    string
}

// Open loads the cache from dir, falling back to the bundled snapshot.
func Open(dir string) *Catalog {
	c := &Catalog{
		path:   filepath.Join(dir, "catalog.json"),
		client: &http.Client{Timeout: 20 * time.Second},
		remote: RemoteURL,
	}
	c.loadBundled()
	c.loadCache()
	return c
}

func (c *Catalog) loadBundled() {
	var s snapshot
	if err := json.Unmarshal(bundled, &s); err != nil {
		return
	}
	c.set(s)
}

func (c *Catalog) loadCache() {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil || len(s.Entries) == 0 {
		return
	}
	c.set(s)
}

func (c *Catalog) set(s snapshot) {
	sort.SliceStable(s.Entries, func(i, j int) bool {
		return len(s.Entries[i].Match) > len(s.Entries[j].Match)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries, c.date = s.Entries, s.SnapshotDate
	if s.FetchedAt > 0 {
		c.fetchedAt = time.UnixMilli(s.FetchedAt)
	}
}

// Date reports the active snapshot's date, for display.
func (c *Catalog) Date() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.fetchedAt.IsZero() {
		return c.fetchedAt.Format("2006-01-02")
	}
	return c.date
}

// Stale reports whether a background refresh is due.
func (c *Catalog) Stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.fetchedAt) > RefreshAfter
}

// Lookup returns the best entry for a model id, and whether one was found.
func (c *Catalog) Lookup(modelID string) (Entry, bool) {
	id := strings.ToLower(modelID)
	// Strip an owner prefix ("anthropic/claude-…", "library/qwen3:8b") and a tag.
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.entries {
		if strings.Contains(id, strings.ToLower(e.Match)) {
			return e, true
		}
	}
	return Entry{}, false
}

// Refresh fetches the remote source and replaces the cache. Safe to call in a goroutine;
// a failure leaves the existing snapshot in place.
func (c *Catalog) Refresh() error {
	req, err := http.NewRequest(http.MethodGet, c.remote, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ferrule")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &http.ProtocolError{ErrorString: "catalog refresh: HTTP " + resp.Status}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	entries, err := parseModelsDev(raw)
	if err != nil || len(entries) == 0 {
		return err
	}
	// The bundled entries stay as the tail so family fallbacks survive a thin remote.
	c.mu.RLock()
	tail := append([]Entry(nil), c.entries...)
	c.mu.RUnlock()
	s := snapshot{
		SnapshotDate: time.Now().Format("2006-01-02"),
		Source:       c.remote,
		FetchedAt:    time.Now().UnixMilli(),
		Entries:      append(entries, tail...),
	}
	c.set(s)
	out, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// parseModelsDev converts models.dev's provider→model map into catalog entries.
// Parsing is deliberately defensive: an upstream shape change degrades to "no remote
// entries", never to a crash or a wiped catalog.
func parseModelsDev(raw []byte) ([]Entry, error) {
	var doc map[string]struct {
		Models map[string]struct {
			ID    string `json:"id"`
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
			Cost struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"cost"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			ToolCall  bool `json:"tool_call"`
			Reasoning bool `json:"reasoning"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range doc {
		for id, m := range p.Models {
			if id == "" {
				continue
			}
			e := Entry{
				Match:      id,
				Context:    m.Limit.Context,
				InCost:     m.Cost.Input,
				OutCost:    m.Cost.Output,
				Modalities: m.Modalities.Input,
			}
			caps := map[string]bool{}
			for _, o := range m.Modalities.Output {
				switch o {
				case "text":
					caps["chat"] = true
				case "image":
					caps["image"] = true
				case "audio":
					caps["audio"] = true
				case "video":
					caps["video"] = true
					e.Async = true
				case "embedding":
					caps["embeddings"] = true
				}
			}
			for _, in := range m.Modalities.Input {
				if in == "image" {
					caps["vision"] = true
				}
			}
			if m.ToolCall {
				caps["tools"] = true
			}
			if m.Reasoning {
				caps["reasoning"] = true
			}
			if len(caps) == 0 {
				caps["chat"] = true
			}
			for k := range caps {
				e.Capabilities = append(e.Capabilities, k)
			}
			sort.Strings(e.Capabilities)
			out = append(out, e)
		}
	}
	return out, nil
}

// SetRemote overrides the refresh URL. Test seam.
func (c *Catalog) SetRemote(u string) { c.remote = u }
