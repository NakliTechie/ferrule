package startup

import (
	"fmt"
	"os/exec"

	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/vault"
)

// Status asks Task Scheduler whether the logon task exists.
func Status(configDir string) State {
	s := State{Supported: true, Unattended: vault.Unattended(configDir), Path: `\` + TaskName}
	if !s.Unattended {
		s.Reason = i18n.T("startup.needsPassphrase")
	}
	s.Enabled = runTool("schtasks", SchtasksQueryArgs()...) == nil
	return s
}

// Enable registers a task that runs the daemon at logon.
func Enable(configDir string, _ ...string) (State, error) {
	if !vault.Unattended(configDir) {
		return Status(configDir), fmt.Errorf("%s", i18n.T("startup.needsPassphrase"))
	}
	exe, err := selfPath()
	if err != nil {
		return State{}, err
	}
	if _, err := exec.LookPath("schtasks"); err != nil {
		return Status(configDir), fmt.Errorf("%s", i18n.T("startup.noSchtasks"))
	}
	if err := runTool("schtasks", SchtasksCreateArgs(exe)...); err != nil {
		return State{}, fmt.Errorf("%s: %w", i18n.T("startup.schtasksFailed"), err)
	}
	return Status(configDir), nil
}

// Disable removes the task. A task that was not there is not an error: the caller asked
// for it to be gone and it is.
func Disable(configDir string) (State, error) {
	_ = runTool("schtasks", SchtasksDeleteArgs()...)
	return Status(configDir), nil
}
