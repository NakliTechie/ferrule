// Package server assembles Ferrule's one HTTP surface: the raw-tokens lane, the media
// passthrough lane, the control API, the embedded panel, and the MCP control face. All
// of them are clients of the same core; there is no second inference path (§4.8).
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"ferrule/internal/api"
	"ferrule/internal/app"
	"ferrule/internal/passthrough"
	"ferrule/internal/router"
	"ferrule/internal/ui"
)

// Options configure the daemon.
type Options struct {
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
		addr = fmt.Sprintf("127.0.0.1:%d", 8899)
	}
	// Localhost only by default (§4.5). A person who wants otherwise says so explicitly
	// in the address they pass.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	router.New(a.DB, a.Vault, a.Catalog).Mount(mux)
	passthrough.New(a.DB, a.Vault).Mount(mux)
	api.New(a).Mount(mux)
	ui.Mount(mux)

	return &Server{
		app: a,
		ln:  ln,
		http: &http.Server{
			Handler:           guardOrigin(a, mux),
			ReadHeaderTimeout: 15 * time.Second,
		},
	}, nil
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
		if isLocalOrigin(origin) || a.DB.Setting("cross_origin", "off") == "on" {
			if a.DB.Setting("cross_origin", "off") == "on" {
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

func isLocalOrigin(origin string) bool {
	o := strings.ToLower(origin)
	for _, p := range []string{"http://localhost", "http://127.0.0.1", "https://localhost", "https://127.0.0.1", "http://[::1]"} {
		if strings.HasPrefix(o, p) {
			return true
		}
	}
	return false
}
