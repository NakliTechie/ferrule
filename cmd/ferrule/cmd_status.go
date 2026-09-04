package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/NakliTechie/ferrule/internal/api"
	"github.com/NakliTechie/ferrule/internal/i18n"
)

// cmdStatus is the single perception act: one bounded read of the whole situation, for a
// person at a terminal or for an agent with --json. Its size grows with the number of
// sources and aliases configured, never with the model catalog or the ledger.
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "structured output, for an agent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := dispatch("brief", api.Args{
		"endpoint": fmt.Sprintf("http://127.0.0.1:%d/v1", envPort()),
	})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	raw, _ := json.Marshal(res)
	var b struct {
		Endpoint string `json:"endpoint"`
		Vault    string `json:"vault"`
		Catalog  struct {
			Date  string `json:"date"`
			Stale bool   `json:"stale"`
		} `json:"catalog"`
		Sources []struct {
			Name     string `json:"name"`
			Where    string `json:"where"`
			Lane     string `json:"lane"`
			Status   string `json:"status"`
			Code     string `json:"code"`
			Reason   string `json:"reason"`
			Remedy   string `json:"remedy"`
			Provider string `json:"provider"`
		} `json:"sources"`
		Models struct {
			Servable     int            `json:"servable"`
			ByWhere      map[string]int `json:"by_where"`
			ByCapability map[string]int `json:"by_capability"`
		} `json:"models"`
		Aliases []struct {
			Name    string `json:"name"`
			Rungs   int    `json:"rungs"`
			Serving string `json:"serving"`
		} `json:"aliases"`
		Grants struct {
			Live  int `json:"live"`
			Total int `json:"total"`
		} `json:"grants"`
		Staged    []map[string]any `json:"staged"`
		Egress24h struct {
			Local struct {
				Requests int `json:"requests"`
			} `json:"local"`
			Cloud struct {
				Requests int `json:"requests"`
			} `json:"cloud"`
		} `json:"egress_24h"`
		Failures []struct {
			App    string `json:"app"`
			Model  string `json:"model"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		} `json:"failures"`
		Next []string `json:"next"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return err
	}

	fmt.Println(i18n.T("cli.status.title", Version, b.Endpoint))
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tWHERE\tLANE\tSTATUS\tDETAIL")
	for _, s := range b.Sources {
		detail := ""
		if s.Status != "live" {
			detail = trimLine(s.Reason)
			if s.Code != "" {
				detail = s.Code + " · " + detail
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Where, s.Lane,
			i18n.SourceStatus(s.Status), detail)
	}
	w.Flush()

	fmt.Printf("\n%d model(s) servable — %d local, %d cloud\n", b.Models.Servable,
		b.Models.ByWhere["local"], b.Models.ByWhere["cloud"])
	if len(b.Models.ByCapability) > 0 {
		fmt.Println("  " + capLine(b.Models.ByCapability))
	}

	if len(b.Aliases) > 0 {
		fmt.Println()
		w = tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ALIAS\tRUNGS\tSERVING")
		for _, a := range b.Aliases {
			serving := a.Serving
			if serving == "" {
				serving = "— every rung is dark"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\n", a.Name, a.Rungs, serving)
		}
		w.Flush()
	}

	fmt.Printf("\n%d app token(s), %d live · last 24h: %d local, %d off-machine\n",
		b.Grants.Total, b.Grants.Live, b.Egress24h.Local.Requests, b.Egress24h.Cloud.Requests)
	fmt.Printf("vault %s · catalog %s%s\n", b.Vault, b.Catalog.Date, staleMark(b.Catalog.Stale))

	if len(b.Failures) > 0 {
		fmt.Println("\nrecent failures:")
		for _, f := range b.Failures {
			fmt.Printf("  %s / %s → %d %s\n", f.App, f.Model, f.Status, trimLine(f.Error))
		}
	}
	fmt.Println("\nnext:")
	for _, n := range b.Next {
		fmt.Println("  " + n)
	}
	return nil
}

func capLine(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, " · ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func staleMark(stale bool) string {
	if stale {
		return " (stale)"
	}
	return ""
}

func trimLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}
