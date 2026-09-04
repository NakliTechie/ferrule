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

// label is the launchd job name. It is also the file name, so changing it strands the
// old registration — which is why it is a constant and not built from anything.
const label = "tech.nakli.ferrule"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Status reports what the login manager says, and whether registering would work.
func Status(configDir string) State {
	s := State{Supported: true, Unattended: vault.Unattended(configDir)}
	if !s.Unattended {
		s.Reason = i18n.T("startup.needsPassphrase")
	}
	path, err := plistPath()
	if err != nil {
		return State{Supported: false, Reason: err.Error()}
	}
	s.Path = path
	if _, err := os.Stat(path); err == nil {
		s.Enabled = true
	}
	return s
}

// Enable registers the daemon with launchd and starts it now.
//
// The plist names the binary by absolute path. A login item that points at a binary that
// has moved is worse than none: it fails silently every morning, and the only sign is
// that the house cannot reach the endpoint.
func Enable(configDir string, args ...string) (State, error) {
	if !vault.Unattended(configDir) {
		return Status(configDir), fmt.Errorf("%s", i18n.T("startup.needsPassphrase"))
	}
	self, err := os.Executable()
	if err != nil {
		return State{}, err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return State{}, err
	}
	path, err := plistPath()
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{}, err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "ferrule.log")

	argv := append([]string{self, "serve"}, args...)
	var b strings.Builder
	b.WriteString(xmlHead)
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", label)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range argv {
		fmt.Fprintf(&b, "    <string>%s</string>\n", escape(a))
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	// A crash comes back; a clean exit stays exited. Plain KeepAlive would make
	// `ferrule` un-stoppable from a terminal, which is its own kind of broken.
	b.WriteString("  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
	b.WriteString("  <key>ProcessType</key><string>Background</string>\n")
	// A bound on any crash loop, whatever causes it. launchd's default is already 10s,
	// but stating it means the plist says what it does rather than inheriting it.
	b.WriteString("  <key>ThrottleInterval</key><integer>10</integer>\n")
	fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n", escape(logPath))
	fmt.Fprintf(&b, "  <key>StandardErrorPath</key><string>%s</string>\n", escape(logPath))
	b.WriteString(xmlTail)

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return State{}, err
	}
	// Replace any previous registration rather than layering on it: bootstrap over a
	// loaded job fails, and the failure reads as "it did not work" when the truth is
	// "it was already working, with the old path".
	_ = run("bootout", domain()+"/"+label)
	if err := run("bootstrap", domain(), path); err != nil {
		// Older systems, and some sandboxed contexts, only speak the legacy verbs.
		if err2 := run("load", "-w", path); err2 != nil {
			os.Remove(path)
			return State{}, fmt.Errorf("%s: %w", i18n.T("startup.launchctlFailed"), err)
		}
	}
	return Status(configDir), nil
}

// Disable takes the registration back out. It does not stop a daemon someone started by
// hand — only the one launchd owns.
func Disable(configDir string) (State, error) {
	path, err := plistPath()
	if err != nil {
		return State{}, err
	}
	_ = run("bootout", domain()+"/"+label)
	_ = run("unload", "-w", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{}, err
	}
	return Status(configDir), nil
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func run(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// escape makes a string safe inside a plist <string>. A home directory can contain an
// ampersand, and a plist that does not parse is a login item that never runs.
func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

const xmlTail = `</dict>
</plist>
`
