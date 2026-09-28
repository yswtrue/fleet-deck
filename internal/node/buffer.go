package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fleetdeck/internal/protocol"
)

// eventBuffer is a crash-safe FIFO of outbound envelopes, one JSON file per
// envelope in dir, named by a zero-padded sequence number. Push is atomic
// (write to a .tmp file, sync, rename) and continues the sequence past the
// highest file found on disk, so order survives restarts. A single consumer
// walks the files in name order and removes one only after the envelope was
// handed off successfully: delivery is at-least-once, and a crash mid-send
// replays the envelope on the next run.
//
// When buffering is enabled the writer sends strictly in sequence order (see
// Node.startBufferedWriter), so wire order equals enqueue order even across
// hub disconnects.
type eventBuffer struct {
	dir string

	mu   sync.Mutex
	seq  uint64
	wake chan struct{}
}

// openEventBuffer prepares dir for use as an event queue: it creates the
// directory, discards stale .tmp files from an interrupted Push, and continues
// the sequence past whatever is already buffered.
func openEventBuffer(dir string) (*eventBuffer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("node: event buffer dir: %w", err)
	}
	b := &eventBuffer{dir: dir, wake: make(chan struct{}, 1)}
	for _, name := range b.entries() {
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(path)
			continue
		}
		var seq uint64
		if _, err := fmt.Sscanf(name, "%d.json", &seq); err == nil && seq > b.seq {
			b.seq = seq
		}
	}
	return b, nil
}

// entries lists queue file names in sequence order (zero-padded names sort
// lexicographically).
func (b *eventBuffer) entries() []string {
	names, err := os.ReadDir(b.dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, e := range names {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// Push appends one envelope to the queue and wakes the writer.
func (b *eventBuffer) Push(env protocol.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("node: marshal buffered envelope: %w", err)
	}
	b.mu.Lock()
	b.seq++
	name := fmt.Sprintf("%020d.json", b.seq)
	b.mu.Unlock()

	tmp := filepath.Join(b.dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(b.dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	b.notify()
	return nil
}

// notify wakes the writer without blocking (one wake is enough: the writer
// re-scans the whole queue on every pass).
func (b *eventBuffer) notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Next returns the oldest buffered envelope and its file path. ok is false
// when the queue is empty. Unreadable or corrupt files are removed and
// skipped so one bad file cannot wedge the queue.
func (b *eventBuffer) Next() (env protocol.Envelope, path string, ok bool) {
	for _, name := range b.entries() {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path = filepath.Join(b.dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(data, &env)
		}
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		return env, path, true
	}
	return protocol.Envelope{}, "", false
}

// Remove deletes a delivered envelope from the queue.
func (b *eventBuffer) Remove(path string) error {
	return os.Remove(path)
}
