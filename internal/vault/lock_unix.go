//go:build !windows

package vault

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking advisory lock. `flock` is the right primitive
// here: the lock is on the open file description, so it is released if the process dies,
// which matters for a daemon a person may kill.
func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
