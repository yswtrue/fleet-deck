package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AcquireLock takes the machine-wide singleton lock for node daemons. It fails
// when another daemon (any config, any hub) already holds it, so a computer
// runs exactly one node process. The lock is an OS file lock: it is released
// automatically if the process crashes, and the holder's pid is written into
// the file for the error message.
//
// path may be empty to disable locking (tests/embedded use); production reads
// Config.LockFile, which defaults to $TMPDIR/fleetdeck-node.lock.
func AcquireLock(path string) (release func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("node: create lock directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, fmt.Errorf("node: open lock %s: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		pid := strings.TrimSpace(readAll(f))
		_ = f.Close()
		hint := ""
		if pid != "" {
			hint = " (pid " + pid + ")"
		}
		return nil, fmt.Errorf(
			"node: another fleetdeck-node is already running%s; only one node process is allowed per machine (lock %s)",
			hint, path,
		)
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockFile(f)
			_ = f.Close()
		})
	}, nil
}

func readAll(f *os.File) string {
	if _, err := f.Seek(0, 0); err != nil {
		return ""
	}
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	return string(buf[:n])
}

// defaultLockFile is the singleton lock location. It is shared by every
// daemon of this user; cross-account duplicates are still stopped by the hub's
// machine fingerprint.
func defaultLockFile() string {
	return filepath.Join(os.TempDir(), "fleetdeck-node.lock")
}
