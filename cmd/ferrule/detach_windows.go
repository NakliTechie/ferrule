//go:build windows

package main

import "os/exec"

// Windows has no session leader to become; a child started without a console window
// already outlives its parent.
func detach(*exec.Cmd) {}
