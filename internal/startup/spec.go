package startup

import (
	"fmt"
	"strings"
)

// The exact text and arguments each platform's login manager is given.
//
// These live outside the build-tagged files on purpose. The part most likely to be wrong
// is quoting and escaping — a path with a space, a home directory with an ampersand — and
// that part is unreachable by a test running on a different operating system if it is
// written inside a //go:build block. Here it is testable everywhere, which matters for
// the two platforms this is developed on a Mac and cannot be run on.

// ServiceName is the identifier used across every platform: the launchd label, the
// systemd unit, the scheduled task. Changing it strands existing registrations, which is
// why it is one constant and not three.
const ServiceName = "tech.nakli.ferrule"

// TaskName is Windows' scheduled-task name. Task Scheduler paths use backslashes, so the
// dotted identifier would read as a folder; this is the same name in that dialect.
const TaskName = "Ferrule"

// SystemdUnit is the user unit that starts the daemon at login.
//
// Restart=on-failure, not always: a clean exit stays exited, so stopping Ferrule from a
// terminal actually stops it. The same choice as launchd's KeepAlive/SuccessfulExit.
func SystemdUnit(exe string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Ferrule — a local key vault and model router\n")
	// Not network-online.target: Ferrule binds a socket and probes local runtimes, and
	// requiring the full network stack delays login on machines that do not need it.
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdEscape(exe))
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=10\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// systemdEscape quotes a path for a unit's ExecStart. systemd splits the line on
// whitespace, so an unquoted path containing a space becomes two arguments and the unit
// fails to start with a message about a binary that does not exist.
func systemdEscape(p string) string {
	if !strings.ContainsAny(p, " \t\"\\'") {
		return p
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// SchtasksCreateArgs registers a logon task on Windows.
//
// Task Scheduler rather than the registry's Run key: a console program started from Run
// flashes a console window at every login, which for a background daemon is noise the
// person cannot get rid of. A logon task runs it without one.
func SchtasksCreateArgs(exe string) []string {
	return []string{
		"/Create",
		"/TN", TaskName,
		// The whole command line is one argument to /TR, so a path with a space has to
		// carry its own quotes inside it.
		"/TR", `"` + exe + `" serve`,
		"/SC", "ONLOGON",
		// Least privilege: this is a user's own daemon and has no business elevated.
		"/RL", "LIMITED",
		// Replace a previous registration rather than failing on it, the same way the
		// other two platforms do — otherwise moving the binary leaves a task pointing at
		// where it used to be and re-registering reports failure.
		"/F",
	}
}

// SchtasksDeleteArgs removes the logon task.
func SchtasksDeleteArgs() []string {
	return []string{"/Delete", "/TN", TaskName, "/F"}
}

// SchtasksQueryArgs asks whether the task exists.
func SchtasksQueryArgs() []string {
	return []string{"/Query", "/TN", TaskName}
}
