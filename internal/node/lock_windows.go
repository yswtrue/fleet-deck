//go:build windows

package node

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes a non-blocking exclusive byte-range lock on the lock file.
func lockFile(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped),
	)
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
