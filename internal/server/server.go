// Package server assembles Ferrule's one HTTP surface: the raw-tokens lane, the media
// passthrough lane, the control API, the embedded panel, and the MCP control face. All
// of them are clients of the same core; there is no second inference path (§4.8).
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"ferrule/internal/api"
	"ferrule/internal/app"
	"ferrule/internal/i18n"
	"ferrule/internal/passthrough"
	"ferrule/internal/router"
	"ferrule/internal/store"
	"ferrule/internal/ui"
)

// Options configure the daemon.
type Options struct {
	// Addr is where the listener binds. The default is every interface, because a box
	// in the house that only the box can use is not what this is for. Whether other
	// machines are actually served is a setting, checked per connection — so the person
	// can turn sharing off from the panel without a restart and without the risk of a
	// rebind that strands the page doing the asking.
	//
	// Binding narrowly is still available and still absolute: --host 127.0.0.1 closes
	// the port to the network entirely, and no setting can reopen it.
	Addr string
}

// Server is the running daemon.
type Server struct {
	app  *app.App
	http *http.Server
	ln   net.Listener
}

// New builds the daemon, binding the listener so the caller learns the real address
// before anything is served.
func New(a *app.App, o Options) (*Server, error) {
	addr := o.Addr
	if addr == "" {
		addr = fmt.Sprintf("0.0.0.0:%d", 8899)
	}
	// The control surface is safe because of who can reach it, not because of what it
	// checks: the panel is handed this run's control token inside the page the daemon
	// serves it, and the MCP door has no authentication of its own because its clients
	// are local agents. Opening the listener to the network without saying so would
	// publish administrative control of every key the person owns.
	//
	// So the control routes answer only to this machine, always, whatever the listener
	// is bound to and whatever the sharing setting says. That check lives on the
	// connection rather than the listener, which is what lets one port and one URL serve
	// the family their inference while the vault stays where it is.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	control := api.New(a)
	srv := &Server{app: a, ln: ln}
	control.SetLANEndpoint(srv.LANEndpoint())
	router.New(a.DB, a.Vault).Mount(mux)
	passthrough.New(a.DB, a.Vault).Mount(mux)
	control.Mount(mux)
	ui.Mount(mux, control.Token().Value())

	srv.http = &http.Server{
		Handler:           guardControlReach(guardShared(a, guardOrigin(a, mux))),
		ReadHeaderTimeout: 15 * time.Second,
	}
	return srv, nil
}

// LANEndpoint is the address other machines should point at, or "" when Ferrule is not
// reachable from the network. The panel quotes it when handing over a new app token, so a
// token minted for someone else arrives with a URL that works from their machine.
func (s *Server) LANEndpoint() string {
	if !s.reachable() {
		return ""
	}
	_, port, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		return ""
	}
	host := outboundIP()
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		} else {
			return ""
		}
	}
	return net.JoinHostPort(host, port)
}

// outboundIP finds the address other machines on this network would reach. It opens a UDP
// socket, which chooses a route without sending anything, rather than guessing from the
// interface list.
func outboundIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9") // TEST-NET-1: routed nowhere, contacted never
	if err != nil {
		return ""
	}
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return ""
	}
	return host
}

// Addr is the address actually bound.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve runs until the context is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdown)
	}()
	err := s.http.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close stops the daemon immediately.
func (s *Server) Close() error { return s.http.Close() }

// controlPath reports whether a path belongs to the control surface rather than to
// inference. Inference authenticates with an app token and is meant to be reachable;
// everything else reads or changes the vault's world and is not.
func controlPath(p string) bool {
	return !strings.HasPrefix(p, "/v1/") && !strings.HasPrefix(p, "/p/")
}

// guardControlReach serves the control surface only to this machine.
//
// The peer address comes from the accepted TCP connection, not from a header, so a client
// cannot claim to be local: replying to a forged source address requires completing a
// handshake the attacker never sees. This is the check that makes one open listener safe
// — inference for the household, the vault for the machine it lives on.
func guardControlReach(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !controlPath(r.URL.Path) || isLoopbackPeer(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, i18n.T("control.notLocal", r.Host), http.StatusForbidden)
	})
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// guardShared serves the inference lanes to the rest of the network only while sharing
// is on. Off does not close the port — the listener is still bound — it refuses the
// request and says so, which is what makes the panel's toggle instant and safe. Someone
// who wants the port shut binds narrowly with --host, and no setting reopens that.
//
// The check is on the accepted connection's peer address, not on a header, so a caller
// cannot claim to be this machine.
func guardShared(a *app.App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackPeer(r.RemoteAddr) || sharingOn(a) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, i18n.T("share.off"), http.StatusForbidden)
	})
}

// sharingOn reads the setting, defaulting to on. A database Ferrule cannot read is not a
// reason to start serving the network: an unreadable setting falls closed.
func sharingOn(a *app.App) bool {
	return a.DB.Setting(store.SetSharing, "on") == "on"
}

// reachable reports whether this listener can be reached from another machine at all —
// a fact about the bind, not about the setting.
func (s *Server) reachable() bool {
	host, _, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsUnspecified() || !ip.IsLoopback())
}

// guardOrigin keeps a random web page from driving the local API. Inference endpoints
// are exempt: they authenticate with an app token, are called by SDKs that send no
// Origin, and are the whole point of the daemon. Control endpoints are not exempt.
func guardOrigin(a *app.App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/p/") {
			next.ServeHTTP(w, r)
			return
		}
		if api.LocalOrigin(origin) || a.DB.Setting(store.SetCrossOrigin, "off") == "on" {
			if a.DB.Setting(store.SetCrossOrigin, "off") == "on" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "cross-origin control requests are off by default — turn on the "+
			"developer setting if you meant this", http.StatusForbidden)
	})
}
