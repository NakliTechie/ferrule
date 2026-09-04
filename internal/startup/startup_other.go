//go:build !darwin && !linux && !windows

package startup

import (
	"fmt"

	"ferrule/internal/i18n"
	"ferrule/internal/vault"
)

// macOS, Linux and Windows are covered. Anywhere else, saying so
// is the whole implementation: a switch that reports success and does nothing is worse
// than one that is honestly absent, because the person only finds out on the morning the
// house cannot reach the endpoint.

func Status(configDir string) State {
	return State{Supported: false, Unattended: vault.Unattended(configDir),
		Reason: i18n.T("startup.unsupported")}
}

func Enable(configDir string, _ ...string) (State, error) {
	return Status(configDir), fmt.Errorf("%s", i18n.T("startup.unsupported"))
}

func Disable(configDir string) (State, error) {
	return Status(configDir), fmt.Errorf("%s", i18n.T("startup.unsupported"))
}
