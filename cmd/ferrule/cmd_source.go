package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"ferrule/internal/api"
	"ferrule/internal/discovery"
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
	"ferrule/internal/store"
)

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for this source (defaults to the provider id)")
	base := fs.String("base-url", "", "base URL, for an unknown OpenAI-compatible endpoint")
	// There is deliberately no --key flag. A key on the command line is a key in your
	// shell history and in the process table, where any other process running as you can
	// read it. The key is read from the terminal without echo, or from stdin when this
	// is piped — which is how a password manager hands one over.
	detect := fs.Bool("detect", false, "scan localhost for running runtimes and adopt them")
	insecure := fs.Bool("insecure", false, "acknowledge that this key will travel over http to a host that is not this machine")
	testModel := fs.String("test-model", "", "the model to prove this source with, when your tier excludes the ones Ferrule would pick")
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}

	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()
	ctx := context.Background()
	bus := api.New(a).Bus()

	a.Discovery.OnStep(func(st discovery.Step) {
		if st.Source != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", st.Source, st.Note)
			return
		}
		fmt.Fprintf(os.Stderr, "  %s\n", st.Note)
	})

	if *detect || len(positional) == 0 {
		fmt.Println(i18n.T("serve.detecting"))
		// Through the bus, like every other door: it is the same core, and this is what
		// puts the operation in the control log.
		raw, err := bus.Dispatch(ctx, "detect_local", api.Args{}, api.DoorCLI, "cli")
		if err != nil {
			return err
		}
		results, err := detectResults(raw)
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
			if !r.Reason.OK() {
				failed++
			}
		}
		if failed == len(results) {
			os.Exit(exitFailed)
		}
		return nil
	}

	pid := positional[0]
	spec, ok := provider.Get(pid)
	if !ok {
		// Returned as a typed Reason, not a formatted string: the exit code is derived
		// from the code, and flattening it here is what made an invocation fault exit 1
		// while the contract said 2.
		return discovery.UnknownProvider(pid)
	}
	// Prompt whenever a key is *possible*, not only where one is mandatory. Plenty of
	// self-hosted OpenAI-compatible servers want a key; refusing to ask left them
	// unaddable through this door with no explanation. An empty answer means no key.
	k := ""
	if spec.NeedsKey || spec.Kind == store.KindCloud {
		prompt := fmt.Sprintf("%s key (%s): ", spec.Label, spec.KeyHint)
		if !spec.NeedsKey {
			prompt = fmt.Sprintf("%s key (%s, blank for none): ", spec.Label, spec.KeyHint)
		}
		if k, err = promptSecret(prompt); err != nil {
			return err
		}
	}
	raw, err := bus.Dispatch(ctx, "add_source", api.Args{
		"name": *name, "provider": pid, "base_url": *base, "key": k,
		"allow_insecure": *insecure, "test_model": *testModel,
	}, api.DoorCLI, "cli")
	if err != nil {
		return err
	}
	r, err := addResult(raw)
	if err != nil {
		return err
	}
	printResult(r)
	if !r.Reason.OK() {
		os.Exit(exitFailed)
	}
	return nil
}

// printResult prints one pipeline outcome. A failure prints its code, its message, and
// its remedy — what happened, what it means, and the exact next move.
type briefSource struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Where    string `json:"where"`
	Lane     string `json:"lane"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

type briefModel struct {
	Model         string   `json:"model"`
	Source        string   `json:"source"`
	Where         string   `json:"where"`
	Capabilities  []string `json:"capabilities"`
	ContextLength int      `json:"context_length"`
	InCost        float64  `json:"in_cost_per_mtok"`
	OutCost       float64  `json:"out_cost_per_mtok"`
}

func sourcesOf(raw any) ([]briefSource, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Sources []briefSource `json:"sources"`
	}
	err = json.Unmarshal(b, &doc)
	return doc.Sources, err
}

func modelsOf(raw any) ([]briefModel, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Models []briefModel `json:"models"`
	}
	err = json.Unmarshal(b, &doc)
	return doc.Models, err
}

// addResult and detectResults re-hydrate the bus's JSON-shaped answer. The bus returns
// what every door sees; the CLI renders it.
func addResult(raw any) (discovery.Result, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return discovery.Result{}, err
	}
	var doc struct {
		Source store.Source     `json:"source"`
		Models int              `json:"models"`
		Reason discovery.Reason `json:"reason"`
		Kept   bool             `json:"kept"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return discovery.Result{}, err
	}
	r := discovery.Result{Source: doc.Source, Reason: doc.Reason, Kept: doc.Kept}
	r.Models = make([]store.Model, doc.Models)
	return r, nil
}

func detectResults(raw any) ([]discovery.Result, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Detected []struct {
			Source store.Source     `json:"source"`
			Models int              `json:"models"`
			Reason discovery.Reason `json:"reason"`
		} `json:"detected"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make([]discovery.Result, 0, len(doc.Detected))
	for _, d := range doc.Detected {
		r := discovery.Result{Source: d.Source, Reason: d.Reason}
		r.Models = make([]store.Model, d.Models)
		out = append(out, r)
	}
	return out, nil
}

func printResult(r discovery.Result) {
	// Keyed on the reason, not on the row: a failed replace leaves the previous source
	// live, and reading success off its status would announce an add that did not happen.
	if r.Reason.OK() {
		fmt.Println(i18n.T("source.added", r.Source.Name, r.Source.Provider,
			i18n.T("source.status.live"), len(r.Models)))
		return
	}
	fmt.Fprintln(os.Stderr, i18n.T("source.failed", r.Source.Name, r.Reason.Message))
	fmt.Fprintln(os.Stderr, "  code:  ", r.Reason.Code)
	if r.Reason.Remedy != "" {
		fmt.Fprintln(os.Stderr, "  remedy:", r.Reason.Remedy)
	}
	if r.Kept {
		fmt.Fprintln(os.Stderr, i18n.T("source.kept", r.Source.Name))
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
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	what := "models"
	if len(positional) > 0 {
		what = positional[0]
	}

	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()
	bus := api.New(a).Bus()
	ctx := context.Background()

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()

	switch what {
	case "sources", "source":
		raw, err := bus.Dispatch(ctx, "list_sources", api.Args{}, api.DoorCLI, "cli")
		if err != nil {
			return err
		}
		srcs, err := sourcesOf(raw)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "NAME\tPROVIDER\tWHERE\tLANE\tSTATUS\tDETAIL")
		for _, s := range srcs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Provider, s.Where, s.Lane,
				i18n.SourceStatus(s.Status), s.Reason)
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
		where := ""
		if *local {
			where = store.KindLocal
		} else if *cloud {
			where = store.KindCloud
		}
		raw, err := bus.Dispatch(ctx, "list_models",
			api.Args{"where": where, "capability": *capFilter}, api.DoorCLI, "cli")
		if err != nil {
			return err
		}
		models, err := modelsOf(raw)
		if err != nil {
			return err
		}
		sort.SliceStable(models, func(i, j int) bool { return models[i].Model < models[j].Model })
		fmt.Fprintln(w, "MODEL\tSOURCE\tWHERE\tCAPABILITIES\tCONTEXT\tIN $/M\tOUT $/M")
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Model, m.Source, m.Where,
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

// cmdRefresh re-runs the pipeline for a source that is already stored, using the key it
// already holds.
//
// Without this verb the only way to retry a source was to add it again and retype the
// key — for a source whose key Ferrule had already accepted, encrypted, and proven. That
// is the friction Ferrule exists to remove, and it bit the first real cloud key: DeepSeek
// answered "insufficient balance", which is a thing you fix at the provider and then ask
// Ferrule to look again.
func cmdRefresh(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return usageErr(i18n.T("cli.needArg", "refresh <source>"))
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()

	src, err := resolveSource(a.DB, positional[0])
	if err != nil {
		return err
	}
	a.Discovery.OnStep(func(st discovery.Step) {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", st.Source, st.Note)
	})
	raw, err := api.New(a).Bus().Dispatch(context.Background(), "refresh_source",
		api.Args{"id": src.ID}, api.DoorCLI, "cli")
	if err != nil {
		return err
	}
	r, err := addResult(raw)
	if err != nil {
		return err
	}
	printResult(r)
	if !r.Reason.OK() {
		os.Exit(exitFailed)
	}
	return nil
}

// cmdRemove deletes a source, its models, and its key.
func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return usageErr(i18n.T("cli.needArg", "rm <source>"))
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()

	src, err := resolveSource(a.DB, positional[0])
	if err != nil {
		return err
	}
	if _, err := api.New(a).Bus().Dispatch(context.Background(), "remove_source",
		api.Args{"id": src.ID}, api.DoorCLI, "cli"); err != nil {
		return err
	}
	fmt.Println(i18n.T("source.removed", src.Name))
	return nil
}

// resolveSource takes what a person would type — the name they gave the source — and
// falls back to the id an agent reading `status --json` would hold.
func resolveSource(db *store.DB, subject string) (store.Source, error) {
	if s, err := db.SourceByName(subject); err == nil {
		return s, nil
	}
	s, err := db.Source(subject)
	if err != nil {
		return store.Source{}, usageErr(i18n.T("cli.noSuchSource", subject))
	}
	return s, nil
}
