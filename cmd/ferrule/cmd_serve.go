package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"ferrule/internal/app"
	"ferrule/internal/config"
	"ferrule/internal/i18n"
	"ferrule/internal/server"
	"ferrule/internal/store"
)

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", envPort(), "port to listen on")
	host := fs.String("host", "127.0.0.1", "address to bind; localhost only by default")
	passphrase := fs.String("passphrase", "", "unlock the vault with a passphrase instead of the on-disk identity")
	noDetect := fs.Bool("no-detect", false, "skip the startup scan for local runtimes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := app.Open(app.Options{Passphrase: *passphrase})
	if err != nil {
		return err
	}
	defer a.Close()

	srv, err := server.New(a, server.Options{Addr: fmt.Sprintf("%s:%d", *host, *port)})
	if err != nil {
		return err
	}

	fmt.Println(i18n.T("serve.listening", srv.Addr()))
	fmt.Println(i18n.T("serve.configDir", a.Dir))
	fmt.Println(i18n.T("serve.vaultBackend", a.Vault.Backend()))

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
	go func() {
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

func envPort() int {
	if v := os.Getenv("FERRULE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return config.DefaultPort
}
