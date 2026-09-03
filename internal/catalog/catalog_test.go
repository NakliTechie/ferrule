package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Two providers can serve different things under the same model id. Picking between them
// by map iteration order means the same model comes back with a different price on
// different runs — in a view whose entire job is telling you what things cost.
func TestPricingIsProviderScopedAndDeterministic(t *testing.T) {
	const remote = `{
      "groq":     {"models": {"llama-3.3-70b": {"id":"llama-3.3-70b",
                   "limit":{"context":131072},"cost":{"input":0.59,"output":0.79},
                   "modalities":{"input":["text"],"output":["text"]}}}},
      "together": {"models": {"llama-3.3-70b": {"id":"llama-3.3-70b",
                   "limit":{"context":8192},"cost":{"input":0.88,"output":0.88},
                   "modalities":{"input":["text"],"output":["text"]}}}}
    }`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remote))
	}))
	defer srv.Close()

	c := Open(t.TempDir())
	c.SetRemote(srv.URL)
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}

	groq, ok := c.Lookup("groq", "llama-3.3-70b")
	if !ok || groq.InCost != 0.59 || groq.Context != 131072 {
		t.Errorf("groq resolved to %+v, want its own price and context", groq)
	}
	tog, ok := c.Lookup("together", "llama-3.3-70b")
	if !ok || tog.InCost != 0.88 || tog.Context != 8192 {
		t.Errorf("together resolved to %+v, want its own price and context", tog)
	}

	// A provider nobody has an entry for falls back to the unscoped family entries, and
	// does so the same way every time.
	first, _ := c.Lookup("", "llama-3.3-70b")
	want := fmt.Sprintf("%+v", first)
	for i := 0; i < 20; i++ {
		again, _ := c.Lookup("", "llama-3.3-70b")
		if got := fmt.Sprintf("%+v", again); got != want {
			t.Fatalf("an unscoped lookup is nondeterministic: %s then %s", want, got)
		}
	}
}

// Refresh rebuilds from the bundled floor. Appending to whatever was in memory meant each
// refresh's remote entries became the next one's tail, so a long-running daemon
// accumulated a copy of the whole catalog per refresh.
func TestRepeatedRefreshesDoNotCompound(t *testing.T) {
	const remote = `{"groq":{"models":{"m1":{"id":"m1","limit":{"context":8},
	                "cost":{"input":1,"output":2},"modalities":{"input":["text"],"output":["text"]}}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remote))
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := Open(dir)
	c.SetRemote(srv.URL)

	sizes := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
		c.mu.RLock()
		sizes = append(sizes, len(c.entries))
		c.mu.RUnlock()
	}
	for i, n := range sizes {
		if n != sizes[0] {
			t.Fatalf("refresh %d holds %d entries, the first held %d — refreshes compound",
				i+1, n, sizes[0])
		}
	}

	// And the cache on disk agrees with memory, because disk is written first.
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != sizes[0] {
		t.Errorf("cache holds %d entries, memory holds %d", len(s.Entries), sizes[0])
	}
}

// A refresh that cannot be written to disk must not be published in memory: a daemon and
// its cache disagreeing about prices outlives the failure that caused it.
func TestAnUnwritableCacheIsNotPublished(t *testing.T) {
	const remote = `{"groq":{"models":{"only-remote":{"id":"only-remote","limit":{"context":8},
	                "cost":{"input":9,"output":9},"modalities":{"input":["text"],"output":["text"]}}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remote))
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := Open(dir)
	c.SetRemote(srv.URL)
	// Make the cache path unwritable by turning it into a directory.
	if err := os.Mkdir(filepath.Join(dir, "catalog.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(); err == nil {
		t.Fatal("an unwritable cache reported success")
	}
	if _, ok := c.Lookup("groq", "only-remote"); ok {
		t.Error("the refresh was published in memory despite failing to write")
	}
}
