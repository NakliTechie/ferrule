package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/NakliTechie/ferrule/internal/config"
	"github.com/NakliTechie/ferrule/internal/i18n"
)

// cmdOpen is what double-clicking Ferrule.app runs: make sure a daemon is up, then show
// the panel.
//
// It lives here rather than in the .app's launcher script because it is the same thing a
// person types in a terminal, and because shell is a bad place for "is it already
// running, and if not, start it and wait for it". The bundle is a thin wrapper around a
// verb that stands on its own.
func cmdOpen(args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	port := fs.Int("port", envPort(), "port the daemon listens on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	if !listening(addr) {
		// Resolved here so a failure can name the actual file rather than a path that is
		// only correct on one operating system.
		logPath, err := logFile()
		if err != nil {
			return err
		}
		if err := spawnDaemon(*port); err != nil {
			return err
		}
		// A cold start opens the database, unlocks the vault and binds the listener. It
		// is fast, but not instant, and opening a browser at a port nothing is on yet
		// shows the person a connection error for something that is about to work.
		if !waitFor(addr, 15*time.Second) {
			return fmt.Errorf("%s", i18n.T("open.noDaemon", addr, logPath))
		}
		fmt.Println(i18n.T("open.started", addr))
	}
	url := "http://" + addr + "/"
	if err := openBrowser(url); err != nil {
		// The daemon is up either way, and that is the load-bearing half. Say where it
		// is rather than treating a browser that would not launch as a failure to start.
		fmt.Println(i18n.T("open.manual", url))
		return nil
	}
	return nil
}

func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func waitFor(addr string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if listening(addr) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// spawnDaemon starts `ferrule serve` detached, so the daemon outlives this process. Its
// output goes to a log file rather than to a terminal that is about to close, or the
// first thing a person loses is the reason it did not start.
func spawnDaemon(port int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logPath, err := logFile()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	cmd := exec.Command(self, "serve", "-port", fmt.Sprint(port))
	cmd.Stdout, cmd.Stderr = f, f
	cmd.Stdin = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Not waited on: this process is about to exit and the daemon is meant to outlive it.
	// Release lets the child go rather than leaving it parented to a dying process.
	return cmd.Process.Release()
}

func logFile() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && runtime.GOOS == "darwin" {
		dir := filepath.Join(home, "Library", "Logs")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return filepath.Join(dir, "ferrule.log"), nil
		}
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ferrule.log"), nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.Command("xdg-open", url).Run()
	}
}
