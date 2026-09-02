package discovery

import (
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
