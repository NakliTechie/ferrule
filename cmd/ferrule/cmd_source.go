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

	"ferrule/internal/discovery"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for this source (defaults to the provider id)")
	base := fs.String("base-url", "", "base URL, for an unknown OpenAI-compatible endpoint")
	key := fs.String("key", "", "API key; omit to be prompted so it stays out of your shell history")
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
		for _, r := range results {
			printResult(r)
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
		os.Exit(1)
	}
	return nil
}

func printResult(r discovery.Result) {
	if r.Source.Status == store.StatusLive {
		fmt.Println(i18n.T("source.added", r.Source.Name, r.Source.Provider,
			i18n.T("source.status.live"), len(r.Models)))
		return
	}
	fmt.Fprintln(os.Stderr, i18n.T("source.failed", r.Source.Name, r.Reason))
}

// promptSecret reads a secret from the terminal. It is read from stdin rather than taken
// as a flag by default so the key never lands in shell history or the process table.
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
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
