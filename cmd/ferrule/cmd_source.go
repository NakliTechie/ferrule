package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"ferrule/internal/discovery"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for this source (defaults to the provider id)")
	base := fs.String("base-url", "", "base URL, for an unknown OpenAI-compatible endpoint")
	key := fs.String("key", "", "API key; omit to be prompted, which keeps it out of your shell history and the process table")
	detect := fs.Bool("detect", false, "scan localhost for running runtimes and adopt them")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()
	ctx := context.Background()

	a.Discovery.OnStep(func(st discovery.Step) {
		if st.Source != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", st.Source, st.Note)
			return
		}
		fmt.Fprintf(os.Stderr, "  %s\n", st.Note)
	})

	if *detect || fs.NArg() == 0 {
		fmt.Println(i18n.T("serve.detecting"))
		results, err := a.Discovery.Detect(ctx)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Println(i18n.T("serve.detectedNone"))
			return nil
		}
		failed := 0
		for _, r := range results {
			printResult(r)
			if r.Source.Status != store.StatusLive {
				failed++
			}
		}
		if failed == len(results) {
			os.Exit(exitFailed)
		}
		return nil
	}

	pid := fs.Arg(0)
	spec, ok := provider.Get(pid)
	if !ok {
		return fmt.Errorf("%s", i18n.T("source.unknownProvider", pid, provider.Names()))
	}
	k := *key
	if spec.NeedsKey && k == "" {
		k, err = promptSecret(fmt.Sprintf("%s key (%s): ", spec.Label, spec.KeyHint))
		if err != nil {
			return err
		}
	}
	r, err := a.Discovery.Add(ctx, discovery.AddRequest{
		Name: *name, Provider: pid, BaseURL: *base, Key: k,
	})
	if err != nil {
		return err
	}
	printResult(r)
	if r.Source.Status == store.StatusFailed {
		os.Exit(exitFailed)
	}
	return nil
}

// printResult prints one pipeline outcome. A failure prints its code, its message, and
// its remedy — what happened, what it means, and the exact next move.
func printResult(r discovery.Result) {
	if r.Source.Status == store.StatusLive {
		fmt.Println(i18n.T("source.added", r.Source.Name, r.Source.Provider,
			i18n.T("source.status.live"), len(r.Models)))
		return
	}
	fmt.Fprintln(os.Stderr, i18n.T("source.failed", r.Source.Name, r.Reason.Message))
	fmt.Fprintln(os.Stderr, "  code:  ", r.Reason.Code)
	if r.Reason.Remedy != "" {
		fmt.Fprintln(os.Stderr, "  remedy:", r.Reason.Remedy)
	}
}

// promptSecret reads a secret from the terminal without echoing it. Reading it here
// rather than taking it as a flag keeps the key out of shell history and out of the
// process table, where any other process on the machine could read it.
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	// Not a terminal: accept a piped secret, which is how scripts and `pass`-style
	// managers hand one over.
	rd := bufio.NewReader(os.Stdin)
	line, err := rd.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	local := fs.Bool("local", false, "only on-machine models")
	cloud := fs.Bool("cloud", false, "only off-machine models")
	capFilter := fs.String("cap", "", "only models with this capability")
	if err := fs.Parse(args); err != nil {
		return err
	}
	what := "models"
	if fs.NArg() > 0 {
		what = fs.Arg(0)
	}

	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()

	switch what {
	case "sources", "source":
		srcs, err := a.DB.Sources()
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "NAME\tPROVIDER\tWHERE\tLANE\tSTATUS\tDETAIL")
		for _, s := range srcs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Provider, s.Kind, s.Lane,
				i18n.SourceStatus(s.Status), s.StatusReason)
		}
		return nil
	case "aliases", "alias":
		as, err := a.DB.Aliases()
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "ALIAS\tLADDER")
		for _, al := range as {
			var rungs []string
			for _, r := range al.Rungs {
				s, err := a.DB.Source(r.SourceID)
				nm := r.SourceID
				if err == nil {
					nm = s.Name
				}
				rungs = append(rungs, nm+"/"+r.ModelID)
			}
			fmt.Fprintf(w, "%s\t%s\n", al.Name, strings.Join(rungs, " → "))
		}
		return nil
	case "models", "model":
		srcs, err := a.DB.Sources()
		if err != nil {
			return err
		}
		byID := map[string]store.Source{}
		for _, s := range srcs {
			byID[s.ID] = s
		}
		models, err := a.DB.Models("")
		if err != nil {
			return err
		}
		sort.SliceStable(models, func(i, j int) bool {
			return models[i].ModelID < models[j].ModelID
		})
		fmt.Fprintln(w, "MODEL\tSOURCE\tWHERE\tCAPABILITIES\tCONTEXT\tIN $/M\tOUT $/M")
		for _, m := range models {
			s := byID[m.SourceID]
			if *local && s.Kind != store.KindLocal {
				continue
			}
			if *cloud && s.Kind != store.KindCloud {
				continue
			}
			if *capFilter != "" && !containsStr(m.Capabilities, *capFilter) {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.ModelID, s.Name, s.Kind,
				strings.Join(m.Capabilities, ","), num(m.ContextLength),
				money(m.InCost), money(m.OutCost))
		}
		return nil
	default:
		return fmt.Errorf("ls: unknown subject %q — try sources, models, or aliases", what)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func num(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func money(f float64) string {
	if f == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", f)
}
