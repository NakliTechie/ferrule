// Package passthrough is Ferrule's media lane (§2.5): the provider's own request and
// response shape, untouched, with the stored key injected and the egress logged.
//
// The unification here is at the vault and observability layer, not the request-shape
// layer. Ferrule does not pretend Replicate is OpenAI-compatible; it just stops you from
// keeping that token in fourteen places.
package passthrough

import (
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/router"
	"ferrule/internal/store"
	"ferrule/internal/vault"
)

// Prefix is the mount root. A source named `replicate` is reached at /p/replicate/…
const Prefix = "/p/"

// Handler serves the passthrough mounts.
type Handler struct {
	db     *store.DB
	vault  vault.Vault
	client *http.Client
}

// New builds a passthrough handler.
func New(db *store.DB, v vault.Vault) *Handler {
	return &Handler{db: db, vault: v, client: &http.Client{
		Timeout: 0,
		// A redirect would re-send the injected provider key to whatever host the
		// response names. Refuse, and hand the caller the redirect to decide about.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Mount registers the media lane on mux.
func (h *Handler) Mount(mux *http.ServeMux) { mux.HandleFunc(Prefix, h.serve) }

// hopByHop headers are connection-scoped and must not be relayed.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		http.Error(w, i18n.T("grant.missing"), http.StatusUnauthorized)
		return
	}
	g, err := h.db.GrantByToken(tok)
	if err != nil || g.Revoked() {
		http.Error(w, i18n.T("grant.rejected"), http.StatusUnauthorized)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, Prefix)
	name, tail, _ := strings.Cut(rest, "/")
	if name == "" {
		http.Error(w, i18n.T("source.notFound", ""), http.StatusNotFound)
		return
	}
	src, err := h.db.SourceByName(name)
	if err != nil {
		http.Error(w, i18n.T("source.notFound", name), http.StatusNotFound)
		return
	}
	// This mount forwards an arbitrary method and path to the provider with the stored
	// key attached. That is the deal for a media source whose shape cannot be normalised
	// — and it must not be the deal for anything else, or an app token would become a
	// general-purpose credential for every provider account the person owns: listing
	// files, deleting fine-tunes, reading billing.
	if src.Lane != store.LanePassthrough {
		http.Error(w, i18n.T("passthrough.wrongLane", src.Name), http.StatusForbidden)
		return
	}
	if src.Status != store.StatusLive {
		http.Error(w, i18n.T("passthrough.notLive", src.Name, src.Status), http.StatusFailedDependency)
		return
	}
	spec, ok := provider.Get(src.Provider)
	if !ok {
		http.Error(w, i18n.T("source.unknownProvider", src.Provider, provider.Names()), http.StatusBadGateway)
		return
	}
	// Scope check before the vault is touched: the key is not fetched for a call this
	// mount will not make.
	if !allowed(src.Provider, r.Method, tail) {
		http.Error(w, i18n.T("passthrough.methodRefused", r.Method+" /"+tail, src.Name),
			http.StatusForbidden)
		return
	}
	key := ""
	if src.KeyRef != "" {
		if key, err = h.vault.Get(src.KeyRef); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	target := provider.URL(src.BaseURL, tail)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Every header the caller sent is relayed verbatim, except the hop-by-hop set and the
	// Ferrule app token, which is replaced by the provider key. Nothing else is touched:
	// that byte-identity is the promise of this lane.
	named := namedByConnection(r.Header)
	for k, vs := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] || named[http.CanonicalHeaderKey(k)] {
			continue
		}
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Api-Key") {
			continue
		}
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}
	upReq.Header.Del("Accept-Encoding")
	spec.Authorize(upReq, key)
	upReq.ContentLength = r.ContentLength

	// Count what is actually sent. ContentLength is -1 for a chunked body, which was
	// being recorded as zero bytes — a silent hole in the one view that is supposed to
	// account for everything that left.
	counted := &countingReader{r: r.Body}
	upReq.Body = io.NopCloser(counted)

	// And classify egress from the address the connection was really made to, not from
	// the URL configured. A name can resolve differently between the two.
	var dialed atomic.Value
	upReq = upReq.WithContext(httptrace.WithClientTrace(upReq.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil {
				dialed.Store(router.Peer(info.Conn.RemoteAddr()))
			}
		},
	}))

	entry := store.Entry{
		GrantID: g.ID, App: g.App, SourceID: src.ID, Provider: src.Provider,
		ModelID: tail, RequestedModel: name + "/" + tail, Lane: store.LanePassthrough,
		Egress: router.Egress(src.BaseURL), ReqBytes: int(max64(r.ContentLength, 0)),
	}
	// Reserved before anything leaves, for the same reason as the token lane: a request
	// the egress view will never show is the one failure this product cannot afford.
	entryID, err := h.db.Begin(entry)
	if err != nil {
		http.Error(w, i18n.T("route.unrecordable", err.Error()), http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	resp, err := h.client.Do(upReq)
	if actual, ok := dialed.Load().(string); ok && actual != "" {
		entry.Egress = actual
	}
	entry.ReqBytes = counted.n
	if err != nil {
		entry.LatencyMS = int(time.Since(start).Milliseconds())
		entry.Status, entry.Err = http.StatusBadGateway, i18n.T("route.upstreamFailed", src.Name, err.Error())
		h.complete(entryID, entry)
		http.Error(w, entry.Err, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respNamed := namedByConnection(resp.Header)
	for k, vs := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] || respNamed[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, copyErr := io.Copy(flushWriter{w}, resp.Body)

	entry.LatencyMS = int(time.Since(start).Milliseconds())
	entry.Status, entry.RespBytes, entry.ReqBytes = resp.StatusCode, int(n), counted.n
	if resp.StatusCode >= 400 {
		entry.Err = i18n.T("reason.bad_status", resp.StatusCode, "")
	}
	if copyErr != nil {
		// The client hung up, or the upstream stopped mid-body. Recording a clean 200
		// for a response that was not delivered whole is the quiet kind of wrong.
		entry.Err = i18n.T("route.truncated", copyErr.Error())
	}
	h.complete(entryID, entry)
}

// complete finishes a reserved ledger row. The response is already served by this point,
// so a failure cannot reach the caller — but the row stays in-flight, which is true, and
// the daemon says so.
func (h *Handler) complete(id int64, e store.Entry) {
	if err := h.db.Complete(id, e); err != nil {
		log.Printf("ferrule: ledger row %d left in-flight: %v", id, err)
	}
}

// countingReader records how many bytes actually went upstream, for a body whose length
// the caller never declared.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// namedByConnection returns the headers a Connection header declares hop-by-hop. RFC 9110
// makes those connection-scoped too, and a proxy that relays them leaks one hop's
// negotiation into the next.
func namedByConnection(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				out[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	return out
}

// flushWriter pushes each chunk out as it arrives so a streaming provider stays streaming.
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return r.Header.Get("X-Api-Key")
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if len(h) > 6 && strings.EqualFold(h[:6], "token ") {
		return strings.TrimSpace(h[6:])
	}
	return strings.TrimSpace(h)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
