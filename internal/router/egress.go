package router

import (
	"net"
	"net/url"
	"strings"

	"github.com/NakliTechie/ferrule/internal/store"
)

// Peer classifies the address a connection was actually made to. This is the
// authoritative answer, taken from the dialer, and it is what the ledger records.
func Peer(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return store.EgressLocal
	}
	return store.EgressCloud
}

// Egress is the pre-flight guess, used to label a request before it is made and to
// describe a source in the interface. It resolves the name itself, which can disagree
// with what the dialer later chooses — DNS can change between the two, and a redirect
// can move the request somewhere else entirely. Where the two disagree, Peer wins and
// the ledger is corrected before it is written.
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
