//go:build !windows

package node

import (
	"os"
	"syscall"
)

// lockFile takes a non-blocking exclusive flock. The lock belongs to the open
// file description, so a second daemon (even in the same process) conflicts.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
