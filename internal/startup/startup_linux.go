package startup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ferrule/internal/i18n"
	"ferrule/internal/vault"
)

const unitName = "ferrule.service"

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

// Status reports what systemd says, and whether registering would work at all.
func Status(configDir string) State {
	s := State{Unattended: vault.Unattended(configDir)}
	if _, err := exec.LookPath("systemctl"); err != nil {
		// A machine without systemd is not a failure to report as one: say what is true
		// and what to do instead, rather than offering a switch that cannot work.
		return State{Supported: false, Unattended: s.Unattended,
			Reason: i18n.T("startup.noSystemd")}
	}
	s.Supported = true
	path, err := unitPath()
	if err != nil {
		return State{Supported: false, Reason: err.Error()}
	}
	s.Path = path
	if _, err := os.Stat(path); err == nil {
		s.Enabled = systemctl("is-enabled", unitName) == nil
	}
	switch {
	case !s.Unattended:
		s.Reason = i18n.T("startup.needsPassphrase")
	case s.Enabled && !lingering():
		// A user unit stops when the last session for that user ends. On a laptop that is
		// invisible; on the headless box somebody put in a cupboard to be the household's
		// Ferrule, it means the daemon dies the moment they log out of ssh — which is the
		// one machine where this switch is the whole point.
		s.Reason = i18n.T("startup.needsLinger", os.Getenv("USER"))
	}
	return s
}

// Enable writes the user unit and starts it now.
func Enable(configDir string, _ ...string) (State, error) {
	if !vault.Unattended(configDir) {
		return Status(configDir), fmt.Errorf("%s", i18n.T("startup.needsPassphrase"))
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return Status(configDir), fmt.Errorf("%s", i18n.T("startup.noSystemd"))
	}
	exe, err := selfPath()
	if err != nil {
		return State{}, err
	}
	path, err := unitPath()
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{}, err
	}
	unit := SystemdUnit(exe)
	if unit == "" {
		return State{}, ErrUnserialisablePath
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return State{}, err
	}
	// daemon-reload before enable, or systemd enables the unit it had cached rather than
	// the one just written — which on a re-register means the old binary path.
	if err := systemctl("daemon-reload"); err != nil {
		return State{}, err
	}
	if err := systemctl("enable", "--now", unitName); err != nil {
		os.Remove(path)
		_ = systemctl("daemon-reload")
		return State{}, fmt.Errorf("%s: %w", i18n.T("startup.systemctlFailed"), err)
	}
	return Status(configDir), nil
}

// Disable stops the unit and takes it back out.
func Disable(configDir string) (State, error) {
	path, err := unitPath()
	if err != nil {
		return State{}, err
	}
	_ = systemctl("disable", "--now", unitName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{}, err
	}
	_ = systemctl("daemon-reload")
	return Status(configDir), nil
}

func systemctl(args ...string) error {
	return runTool("systemctl", append([]string{"--user"}, args...)...)
}

// lingering reports whether this user's services survive their last logout.
func lingering() bool {
	out, err := exec.Command("loginctl", "show-user", os.Getenv("USER"),
		"--property=Linger").Output()
	if err != nil {
		return true // loginctl absent or unhappy: do not invent a warning
	}
	return strings.TrimSpace(string(out)) == "Linger=yes"
}
