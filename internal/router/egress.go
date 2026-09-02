package router

import (
	"net"
	"net/url"
	"strings"

	"ferrule/internal/store"
)

// Egress answers the only question the egress view exists to answer: did this request
// leave the machine? It is decided from the URL actually dialled, not from how the
// source was labelled — an "OpenAI-compatible" source pointed at a box down the hall is
// off-machine, and saying otherwise would be the one lie this dashboard cannot afford.
func Egress(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return store.EgressCloud
	}
	host := u.Hostname()
	if host == "" {
		return store.EgressCloud
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return store.EgressLocal
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A name that is not localhost has to be resolved to be judged. Resolution is
		// cheap and cached by the OS; an unresolvable name is treated as off-machine.
		addrs, err := net.LookupIP(host)
		if err != nil || len(addrs) == 0 {
			return store.EgressCloud
		}
		for _, a := range addrs {
			if !a.IsLoopback() {
				return store.EgressCloud
			}
		}
		return store.EgressLocal
	}
	if ip.IsLoopback() {
		return store.EgressLocal
	}
	return store.EgressCloud
}
