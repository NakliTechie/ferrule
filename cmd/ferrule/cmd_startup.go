package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/NakliTechie/ferrule/internal/api"
	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/startup"
)

// cmdStartup registers or unregisters the daemon with the login manager.
//
//	ferrule startup          what it is now
//	ferrule startup on       start when I log in
//	ferrule startup off      stop doing that
func cmdStartup(args []string) error {
	fs := flag.NewFlagSet("startup", flag.ContinueOnError)
	positional, err := parseWithSubject(fs, args)
	if err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.Close()

	want := ""
	if len(positional) > 0 {
		want = positional[0]
	}
	var st startup.State
	switch want {
	case "":
		st = startup.Status(a.Dir)
	case "on", "off":
		// Through the bus like every other mutation, so it lands in the control log.
		raw, err := api.New(a).Bus().Dispatch(context.Background(), "set_startup",
			api.Args{"value": want}, api.DoorCLI, "cli")
		if err != nil {
			return err
		}
		st = stateFrom(raw)
	default:
		return usageErr(i18n.T("cli.needArg", "startup [on|off]"))
	}

	if st.Enabled {
		fmt.Println(i18n.T("startup.on"))
		if st.Path != "" {
			fmt.Println("  " + st.Path)
		}
	} else {
		fmt.Println(i18n.T("startup.off"))
	}
	// The reason is context, not an error, and it belongs after the line it explains.
	// On stderr it raced the line above and printed first.
	if st.Reason != "" {
		fmt.Println("  " + st.Reason)
	}
	return nil
}

func stateFrom(raw any) startup.State {
	b, err := json.Marshal(raw)
	if err != nil {
		return startup.State{}
	}
	var doc struct {
		Startup startup.State `json:"startup"`
	}
	_ = json.Unmarshal(b, &doc)
	return doc.Startup
}
