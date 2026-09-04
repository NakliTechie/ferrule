// Package netutil holds the one question several packages ask about a connection.
package netutil

import (
	"net"
	"strings"
)

// IsLoopbackPeer reports whether a request came from this machine.
//
// The address is the accepted TCP connection's, never a header, so a caller cannot claim
// to be local: replying to a forged source address requires completing a handshake the
// sender never sees. Both the control-surface guard and the decision about how much of a
// failure to hand back turn on this, and two copies of it would be two chances to drift.
func IsLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
