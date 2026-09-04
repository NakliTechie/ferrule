// Command ferrule is a local key vault and model router.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/discovery"
	"github.com/NakliTechie/ferrule/internal/i18n"
)

// Version is stamped at build time: go build -ldflags "-X main.Version=v1.0.0".
var Version = "dev"

func main() {
	if err := i18n.LoadError(); err != nil {
		fmt.Fprintln(os.Stderr, "ferrule: strings:", err)
		os.Exit(2)
	}
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(0)
	}
	verb, rest := args[0], args[1:]

	var err error
	switch verb {
	case "status":
		err = cmdStatus(rest)
	case "serve":
		err = cmdServe(rest)
	case "add":
		err = cmdAdd(rest)
	case "ls", "list":
		err = cmdLs(rest)
	case "open":
		err = cmdOpen(rest)
	case "startup":
		err = cmdStartup(rest)
	case "refresh":
		err = cmdRefresh(rest)
	case "rm", "remove":
		err = cmdRemove(rest)
	case "alias":
		err = cmdAlias(rest)
	case "remap":
		err = cmdRemap(rest)
	case "key":
		err = cmdKey(rest)
	case "usage":
		err = cmdUsage(rest)
	case "export":
		err = cmdExport(rest)
	case "import":
		err = cmdImport(rest)
	case "version", "--version", "-v":
		fmt.Println("ferrule", Version)
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("%s", i18n.T("cli.unknownVerb", verb))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferrule:", err)
		// A typed failure carries the next move; printing the problem without it is
		// half the message.
		var r discovery.Reason
		if errors.As(err, &r) && r.Remedy != "" {
			fmt.Fprintln(os.Stderr, " ", r.Remedy)
		}
		os.Exit(exitFor(err))
	}
}

// Exit codes map one-to-one to what the caller should do next, so nobody has to parse a
// message to find out (DRIVER §3):
//
//	0  the thing worked
//	1  it did not, and the reason says why — fix the cause and re-run
//	2  the command itself was wrong — fix the invocation
const (
	exitOK      = 0
	exitFailed  = 1
	exitBadArgs = 2
)

// usageErr marks an invocation fault — the command was wrong, not the world. It exits 2,
// which is the contract's "fix the command", rather than 1's "fix the cause and re-run".
type usageErr string

func (e usageErr) Error() string { return string(e) }

func exitFor(err error) int {
	var u usageErr
	if errors.As(err, &u) {
		return exitBadArgs
	}
	var r discovery.Reason
	if errors.As(err, &r) {
		switch r.Code {
		case discovery.CodeUnknownProvider, discovery.CodeNeedsKey, discovery.CodeNeedsBaseURL:
			return exitBadArgs
		}
		return exitFailed
	}
	if errors.Is(err, flag.ErrHelp) || isUsage(err) {
		return exitBadArgs
	}
	return exitFailed
}

func isUsage(err error) bool {
	m := err.Error()
	return strings.HasPrefix(m, "unknown verb") ||
		strings.Contains(m, "flag provided but not defined") ||
		strings.Contains(m, "needs an argument")
}

func usage() {
	fmt.Println(i18n.T("cli.usage"))
	fmt.Println()
	fmt.Println(i18n.T("cli.usage.verbs"))
}

// open brings up the core for a one-shot CLI verb.
func open() (*app.App, error) { return app.Open(app.Options{}) }
