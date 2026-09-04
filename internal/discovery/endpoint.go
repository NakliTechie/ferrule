package discovery

import (
	"ferrule/internal/secrets"
	"net"
	"net/url"
	"strings"
)

// checkEndpoint refuses a base URL that would put a key on the wire in the clear.
//
// A curated provider's default is https, but a person (or an agent, or a mistake in a
// script) can point `anthropic` at any URL they like, and the pipeline will dutifully
// attach the stored Anthropic key to the first request. Over http to anything but this
// machine, that is a key handed to whoever is on the path.
//
// Loopback is exempt because a local runtime speaks http by definition and the packets
// never reach an interface anyone can read.
func checkEndpoint(baseURL string, needsKey bool) Reason {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return newReason(CodeNeedsBaseURL)
	}
	// A base URL is stored in SQLite and travels in a configuration export, so it must
	// never be a place to hide a credential. `https://user:pass@host/v1` and
	// `https://host/v1?api_key=…` both put one outside the vault, which is the one
	// boundary this product exists to hold.
	if u.User != nil {
		return newReason(CodeCredentialInURL, "userinfo")
	}
	// A fragment is never sent to a server, so nothing legitimate puts one on a base URL.
	if u.Fragment != "" {
		return newReason(CodeCredentialInURL, "fragment")
	}
	for k, vals := range u.Query() {
		l := strings.ToLower(k)
		if strings.Contains(l, "key") || strings.Contains(l, "token") ||
			strings.Contains(l, "secret") || strings.Contains(l, "password") ||
			strings.Contains(l, "auth") {
			return newReason(CodeCredentialInURL, k)
		}
		// The name is the part the person chooses freely; the value is the part that is
		// the key. Checking only names let `?x=sk-live-…` through, and the URL was then
		// written to sources.base_url and quoted back in status_reason in plaintext —
		// outside the vault, which is the one boundary this product exists to hold.
		for _, v := range vals {
			if secrets.Looks(v) {
				return newReason(CodeCredentialInURL, k)
			}
		}
	}
	if secrets.Looks(u.Path) {
		return newReason(CodeCredentialInURL, "path")
	}
	if u.Scheme == "https" {
		return Reason{Code: CodeOK}
	}
	if isLoopbackHost(u.Hostname()) {
		return Reason{Code: CodeOK}
	}
	if !needsKey {
		// No key is attached, so there is nothing to leak. A person pointing at a
		// keyless OpenAI-compatible server on their LAN is doing something reasonable.
		return Reason{Code: CodeOK}
	}
	return newReason(CodeInsecureURL, u.Host)
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
