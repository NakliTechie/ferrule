package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"ferrule/internal/api"
	"ferrule/internal/config"
	"ferrule/internal/i18n"
)

// dispatch runs a control op through the same command bus the panel and the agent face
// use. The CLI is a client of the bus, not a parallel implementation (§4.8).
func dispatch(name string, args api.Args) (any, error) {
	a, err := open()
	if err != nil {
		return nil, err
	}
	defer a.Close()
	return api.New(a).Bus().Dispatch(context.Background(), name, args, api.DoorCLI, "cli")
}

func cmdAlias(args []string) error {
	fs := flag.NewFlagSet("alias", flag.ContinueOnError)
	remove := fs.Bool("rm", false, "remove the alias")
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		res, err := dispatch("list_aliases", api.Args{})
		if err != nil {
			return err
		}
		return printAliases(res)
	}
	name := positional[0]
	if *remove {
		_, err := dispatch("remove_alias", api.Args{"name": name})
		if err != nil {
			return err
		}
		fmt.Println(i18n.T("alias.removed", name))
		return nil
	}
	if len(positional) == 1 {
		res, err := dispatch("get_alias", api.Args{"name": name})
		if err != nil {
			return err
		}
		return printAliases(map[string]any{"aliases": []any{res}})
	}
	ladder := make([]any, 0, len(positional)-1)
	for _, r := range positional[1:] {
		ladder = append(ladder, r)
	}
	res, err := dispatch("set_alias", api.Args{"name": name, "ladder": ladder})
	if err != nil {
		return err
	}
	m, _ := res.(map[string]any)
	fmt.Println(i18n.T("alias.set", name, describeLadder(m)))
	return nil
}

func describeLadder(m map[string]any) string {
	rungs, _ := m["rungs"].([]map[string]any)
	var parts []string
	for _, r := range rungs {
		parts = append(parts, fmt.Sprintf("%v/%v", r["source"], r["model"]))
	}
	return strings.Join(parts, " → ")
}

func printAliases(res any) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	var doc struct {
		Aliases []struct {
			Name  string `json:"name"`
			Rungs []struct {
				Source    string `json:"source"`
				Model     string `json:"model"`
				Available bool   `json:"available"`
				Reason    string `json:"reason"`
			} `json:"rungs"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "ALIAS\tLADDER")
	for _, a := range doc.Aliases {
		var parts []string
		for _, r := range a.Rungs {
			s := r.Source + "/" + r.Model
			if !r.Available {
				s = "(" + s + ")"
			}
			parts = append(parts, s)
		}
		fmt.Fprintf(w, "%s\t%s\n", a.Name, strings.Join(parts, " → "))
	}
	return nil
}

func cmdRemap(args []string) error {
	fs := flag.NewFlagSet("remap", flag.ContinueOnError)
	remove := fs.Bool("rm", false, "remove the remap")
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return printJSON(dispatch("list_remaps", api.Args{}))
	}
	if *remove {
		_, err := dispatch("remove_remap", api.Args{"from": positional[0]})
		return err
	}
	if len(positional) < 2 {
		return fmt.Errorf("%s", i18n.T("cli.needArg", "remap <from-model> <alias | source/model>"))
	}
	return printJSON(dispatch("set_remap", api.Args{"from": positional[0], "to": positional[1]}))
}

func cmdKey(args []string) error {
	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	revoke := fs.String("revoke", "", "revoke the app token with this id")
	list := fs.Bool("ls", false, "list app tokens")
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	if *revoke != "" {
		if _, err := dispatch("revoke_grant", api.Args{"id": *revoke}); err != nil {
			return err
		}
		fmt.Println(i18n.T("grant.revoked", *revoke))
		return nil
	}
	if *list || len(positional) == 0 {
		return printGrants()
	}
	res, err := dispatch("mint_grant", api.Args{"app": positional[0]})
	if err != nil {
		return err
	}
	m, _ := res.(map[string]any)
	tok, _ := m["token"].(string)
	fmt.Println(i18n.T("grant.minted", positional[0], tok,
		fmt.Sprintf("127.0.0.1:%d", envPort())))
	return nil
}

func printGrants() error {
	res, err := dispatch("list_grants", api.Args{})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(res)
	var doc struct {
		Grants []struct {
			ID       string  `json:"id"`
			App      string  `json:"app"`
			Revoked  bool    `json:"revoked"`
			Requests int     `json:"requests"`
			Cost     float64 `json:"cost"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "ID\tAPP\tSTATUS\tREQUESTS\tCOST")
	for _, g := range doc.Grants {
		status := i18n.T("source.status.live")
		if g.Revoked {
			status = i18n.T("ui.grants.revoked")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t$%.4f\n", g.ID, g.App, status, g.Requests, g.Cost)
	}
	return nil
}

func cmdUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	by := fs.String("by", "app,model", "group by any of app, model, source, provider, egress")
	hours := fs.Int("hours", 0, "window in hours; 0 for all time")
	egress := fs.Bool("egress", false, "show what left the machine")
	content := fs.Int("content", 0, "print the last N logged request/response pairs, if content logging is on")
	forget := fs.Bool("forget-content", false, "delete everything the content log holds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *forget {
		res, err := dispatch("forget_content", api.Args{})
		if err != nil {
			return err
		}
		m, _ := res.(map[string]any)
		fmt.Println(m["message"])
		return nil
	}
	if *content > 0 {
		return printContent(*content)
	}
	if *egress {
		return printEgress(*hours)
	}
	cols := make([]any, 0)
	for _, c := range strings.Split(*by, ",") {
		if c = strings.TrimSpace(c); c != "" {
			cols = append(cols, c)
		}
	}
	res, err := dispatch("usage_summary", api.Args{"by": cols, "since_hours": float64(*hours)})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(res)
	var doc struct {
		Buckets []struct {
			Key              string  `json:"key"`
			Requests         int     `json:"requests"`
			Errors           int     `json:"errors"`
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
			AvgLatencyMS     int     `json:"avg_latency_ms"`
		} `json:"buckets"`
		Total struct {
			Requests int     `json:"requests"`
			Cost     float64 `json:"cost"`
		} `json:"total"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Buckets) == 0 {
		fmt.Println(i18n.T("usage.empty"))
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "GROUP\tREQUESTS\tERRORS\tTOKENS\tLATENCY\tCOST")
	for _, b := range doc.Buckets {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%dms\t$%.4f\n", b.Key, b.Requests, b.Errors,
			b.PromptTokens+b.CompletionTokens, b.AvgLatencyMS, b.Cost)
	}
	fmt.Fprintf(w, "%s\t%d\t\t\t\t$%.4f\n", i18n.T("ui.usage.total"), doc.Total.Requests, doc.Total.Cost)
	return nil
}

func printContent(limit int) error {
	res, err := dispatch("read_content", api.Args{"limit": float64(limit)})
	if err != nil {
		return err
	}
	m, _ := res.(map[string]any)
	if msg, ok := m["message"].(string); ok {
		fmt.Println(msg)
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(m["content"])
}

func printEgress(hours int) error {
	res, err := dispatch("egress_summary", api.Args{"since_hours": float64(hours)})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(res)
	var doc struct {
		Local struct {
			Requests  int `json:"requests"`
			ReqBytes  int `json:"req_bytes"`
			RespBytes int `json:"resp_bytes"`
		} `json:"local"`
		Cloud struct {
			Requests  int `json:"requests"`
			ReqBytes  int `json:"req_bytes"`
			RespBytes int `json:"resp_bytes"`
		} `json:"cloud"`
		Detail []struct {
			Key      string `json:"key"`
			Requests int    `json:"requests"`
		} `json:"detail"`
		OffShare float64 `json:"off_machine_share"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "WHERE\tREQUESTS\tBYTES")
	fmt.Fprintf(w, "%s\t%d\t%d\n", i18n.T("usage.egress.local"), doc.Local.Requests,
		doc.Local.ReqBytes+doc.Local.RespBytes)
	fmt.Fprintf(w, "%s\t%d\t%d\n", i18n.T("usage.egress.cloud"), doc.Cloud.Requests,
		doc.Cloud.ReqBytes+doc.Cloud.RespBytes)
	fmt.Fprintf(w, "\noff-machine share\t%.0f%%\n", doc.OffShare*100)
	return nil
}

func cmdExport(args []string) error {
	path := config.ExportDefault
	if len(args) > 0 {
		path = args[0]
	}
	pass, err := promptSecret("Passphrase to seal this export (8+ characters): ")
	if err != nil {
		return err
	}
	res, err := dispatch("export_config", api.Args{"path": path, "passphrase": pass})
	if err != nil {
		return err
	}
	m, _ := res.(map[string]any)
	fmt.Println(i18n.T("export.written", m["path"]))
	return nil
}

func cmdImport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", i18n.T("cli.needArg", "import <file>"))
	}
	pass, err := promptSecret("Passphrase this file was sealed with: ")
	if err != nil {
		return err
	}
	res, err := dispatch("import_config", api.Args{"path": args[0], "passphrase": pass})
	if err != nil {
		return err
	}
	m, _ := res.(map[string]any)
	fmt.Println(i18n.T("import.read", args[0], m["sources"], m["aliases"], m["grants"]))
	return nil
}

func printJSON(res any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
