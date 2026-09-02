// Package ui serves the embedded control surface. The panel is part of the binary
// (go:embed), so "one binary" stays literally true and the daemon fetches nothing from
// the network to render itself — fonts included.
package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"ferrule/internal/i18n"
	"ferrule/internal/provider"
)

//go:embed assets
var assets embed.FS

// Mount registers the panel and its assets.
func Mount(mux *http.ServeMux) {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))

	mux.HandleFunc("/ui/strings.json", serveStrings)
	mux.HandleFunc("/ui/providers.json", serveProviders)
	mux.Handle("/ui/", cache(http.StripPrefix("/ui/", files)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The panel talks only to this daemon. Saying so in a header means a mistake in
		// the panel cannot quietly become an outbound request.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		raw, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	})
}

func cache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".woff2") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h.ServeHTTP(w, r)
	})
}

// serveStrings hands the panel the active locale's table. No user-facing copy is written
// into the panel's own source.
func serveStrings(w http.ResponseWriter, r *http.Request) {
	table := map[string]string{}
	for _, k := range i18n.Keys() {
		table[k] = i18n.T(k)
	}
	writeJSON(w, table)
}

// serveProviders hands the panel the curated seed set, so the add-a-source form is
// generated from the same data the pipeline routes with.
func serveProviders(w http.ResponseWriter, r *http.Request) {
	specs := provider.All()
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		out = append(out, map[string]any{
			"id": s.ID, "label": s.Label, "where": s.Kind, "lane": s.Lane,
			"default_base_url": s.DefaultBaseURL, "needs_key": s.NeedsKey,
			"needs_base_url": s.NeedsBaseURL, "key_hint": s.KeyHint, "docs": s.Docs,
		})
	}
	writeJSON(w, map[string]any{"providers": out})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(v)
}
