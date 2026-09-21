package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guide ships inside the binary and is served by the daemon, so a person offline has
// the whole of it. This is the check that the embed still names a real, built guide:
// docs/index.html is generated, and a tree where it is missing does not compile, but a
// tree where it is empty or stale would.
func TestTheGuideIsServedFromTheBinary(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, "test-token")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/guide/", "/guide/index.html"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(body), "<title>Ferrule — the guide</title>") || !strings.Contains(string(body), `class="card`) {
			t.Fatalf("%s does not look like the built guide (%d bytes)", path, len(body))
		}
	}
	// Every capture the page references must be in the binary too, or the page is a
	// gallery of broken images for the person it was embedded for.
	res, _ := http.Get(srv.URL + "/guide/")
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	missing := 0
	for _, ref := range refs(string(page)) {
		r, err := http.Get(srv.URL + "/guide/" + ref)
		if err != nil || r.StatusCode != 200 {
			missing++
			t.Errorf("%s: not served", ref)
		}
		if r != nil {
			r.Body.Close()
		}
	}
	if missing > 0 {
		t.Fatalf("%d referenced captures missing from the embed", missing)
	}
}

// refs pulls the relative screenshot paths out of the page.
func refs(page string) []string {
	var out []string
	for _, part := range strings.Split(page, `src="screenshots/`)[1:] {
		out = append(out, "screenshots/"+part[:strings.Index(part, `"`)])
	}
	return out
}
