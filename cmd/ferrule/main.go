// Command ferrule is a local key vault and model router.
package main

import (
	"fmt"
	"os"

	"ferrule/internal/app"
	"ferrule/internal/i18n"
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
	case "serve":
		err = cmdServe(rest)
	case "add":
		err = cmdAdd(rest)
	case "ls", "list":
		err = cmdLs(rest)
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
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(i18n.T("cli.usage"))
	fmt.Println()
	fmt.Println(i18n.T("cli.usage.verbs"))
}

// open brings up the core for a one-shot CLI verb.
func open() (*app.App, error) { return app.Open(app.Options{}) }
