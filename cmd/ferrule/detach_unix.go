//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own process group, so closing the terminal that started
// it — or the .app launcher exiting — does not take it down with them.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
