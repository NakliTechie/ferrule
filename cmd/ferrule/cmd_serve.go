package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/config"
	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/server"
	"github.com/NakliTechie/ferrule/internal/store"
)

// ferruleOn reports whether the thing holding a port is a Ferrule daemon. It asks the
// unauthenticated inference endpoint, which answers every caller with Ferrule's own error
// shape and needs no control token to recognise.
func ferruleOn(port int) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/models", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return bytes.Contains(body, []byte("ferrule_error"))
}

// serveArgs reproduces this invocation, minus what a fresh start works out for itself.
// Only what differs from the default is recorded, so a login item registered on a plain
// `ferrule serve` stays a plain `ferrule serve`.
func serveArgs(host string, port int, advertise string, noDetect bool, dir string) []string {
	var out []string
	if def, err := config.Dir(); err != nil || dir != def {
		// Only when it is not where a fresh start would look. Recording the default would
		// pin the login item to a path that is otherwise free to move.
		out = append(out, "-config-dir", dir)
	}
	if host != "0.0.0.0" {
		out = append(out, "-host", host)
	}
	if port != config.DefaultPort {
		out = append(out, "-port", fmt.Sprint(port))
	}
	if advertise != "" {
		out = append(out, "-advertise", advertise)
	}
	if noDetect {
		out = append(out, "-no-detect")
	}
	return out
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", envPort(), "port to listen on")
	host := fs.String("host", "0.0.0.0", "address to bind; use 127.0.0.1 to close the port to the network entirely")
	passphrase := fs.Bool("passphrase", false, "prompt for a passphrase to unlock the vault, so nothing that can open it is written to disk")
	noDetect := fs.Bool("no-detect", false, "skip the startup scan for local runtimes")
	// FERRULE_CONFIG_DIR is the general mechanism and an environment variable cannot be
	// carried into a launchd plist, a systemd unit and a scheduled task uniformly. This
	// flag is how a login item says out loud which Ferrule it is starting.
	configDir := fs.String("config-dir", "", "where Ferrule keeps its state; defaults to FERRULE_CONFIG_DIR or the usual place")
	advertise := fs.String("advertise", "", "the address to hand out instead of a detected one — a hostname survives DHCP, an address does not")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pass := ""
	if *passphrase {
		var err error
		// Prompted, never a flag value: a passphrase on the command line is a passphrase
		// in the process table.
		if pass, err = promptSecret("Vault passphrase: "); err != nil {
			return err
		}
	}
	a, err := app.Open(app.Options{Passphrase: pass, Dir: *configDir})
	if err != nil {
		return err
	}
	defer a.Close()

	srv, err := server.New(a, server.Options{
		Addr: fmt.Sprintf("%s:%d", *host, *port), Advertise: *advertise,
		ServeArgs: serveArgs(*host, *port, *advertise, *noDetect, a.Dir),
	})
	if err != nil {
		// A login item and a double-clicked app both want to make sure Ferrule is
		// running, and both are right when it already is. Treating "another Ferrule has
		// this port" as a failure made launchd restart the job forever against a port
		// that was never going to free up — a crash loop on a family's machine, visible
		// only as a log file growing overnight.
		//
		// Another program on the port is a different thing entirely and stays an error.
		if errors.Is(err, syscall.EADDRINUSE) && ferruleOn(*port) {
			fmt.Println(i18n.T("serve.alreadyRunning", *port))
			return nil
		}
		return err
	}
	// The household key exists from the first start, because the first thing a person
	// does with this is give it to somebody. Minting it on demand would put a step
	// between them and the thing the product is for.
	if _, _, err := a.HouseholdKey(); err != nil {
		return err
	}

	fmt.Println(i18n.T("serve.listening", localAddr(srv.Addr(), *port)))
	fmt.Println(i18n.T("serve.configDir", a.Dir))
	fmt.Println(i18n.T("serve.vaultBackend", a.Vault.Backend()))
	if ep := srv.LANEndpoint(); ep != "" {
		fmt.Println()
		on := a.DB.Setting(store.SetSharing, "on") == "on"
		if on {
			fmt.Println(i18n.T("serve.lanListening", ep))
		} else {
			fmt.Println(i18n.T("serve.lanOff", ep))
		}
		fmt.Println("  " + i18n.T("serve.lanNote"))
		fmt.Println("  " + i18n.T("serve.lanToken"))
		fmt.Println("  " + i18n.T("serve.lanCleartext"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Detection and the catalog refresh run behind the surface. The panel paints
	// immediately and fills in; neither is allowed to hold the first frame.
	if !*noDetect {
		go func() {
			res, err := a.Discovery.Detect(context.Background())
			if err != nil {
				return
			}
			live := 0
			for _, r := range res {
				if r.Source.Status == store.StatusLive {
					live++
				}
			}
			if live == 0 {
				fmt.Println(i18n.T("serve.detectedNone"))
				return
			}
			fmt.Println(i18n.T("serve.detected", live))
		}()
	}
	// The catalog refresh is the one request Ferrule makes on its own behalf. It carries
	// nothing about the person — no key, no prompt, no identifier — but it is still an
	// outbound request to a third party, so it is disclosed at start and can be turned
	// off, rather than being quietly excluded from "nothing phones home".
	if a.DB.Setting(store.SetCatalogRefresh, "on") != "on" {
		fmt.Println(i18n.T("serve.catalogOff", a.Catalog.Date()))
	}
	go func() {
		if a.DB.Setting(store.SetCatalogRefresh, "on") != "on" {
			return
		}
		if a.Catalog.Stale() {
			_ = a.Catalog.Refresh()
		}
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if a.DB.Setting(store.SetCatalogRefresh, "on") != "on" {
					continue
				}
				if a.Catalog.Stale() {
					_ = a.Catalog.Refresh()
				}
			}
		}
	}()

	err = srv.Serve(ctx)
	fmt.Println(i18n.T("serve.shutdown"))
	return err
}

// localAddr keeps the printed URL usable from this machine even when the listener is
// bound to every interface: "0.0.0.0:8899" is not somewhere a browser can go.
func localAddr(bound string, port int) string {
	if strings.HasPrefix(bound, "0.0.0.0:") || strings.HasPrefix(bound, "[::]:") {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	return bound
}

func envPort() int {
	if v := os.Getenv("FERRULE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return config.DefaultPort
}
