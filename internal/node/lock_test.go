package node

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSingletonLock pins the one-process-per-machine guard: a second daemon
// cannot take the lock until the first releases it.
func TestSingletonLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.lock")

	release, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := AcquireLock(path); err == nil {
		t.Fatal("second acquire succeeded, want lock conflict")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire error = %v, want 'already running'", err)
	}

	release()
	releaseAgain, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	releaseAgain()
}

// TestMachineIDStable pins the fingerprint contract the hub relies on:
// non-empty, hash-prefixed, and identical across calls.
func TestMachineIDStable(t *testing.T) {
	first := detectMachineID()
	second := detectMachineID()
	if first == "" {
		t.Fatal("detected machine id is empty")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("machine id = %q, want sha256: prefix", first)
	}
	if first != second {
		t.Fatalf("machine id changed between calls: %q != %q", first, second)
	}
}
