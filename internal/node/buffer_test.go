package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
)

// deltaText reads a SessionDelta out of a MsgEvent envelope so tests can track
// replay order.
func deltaText(t *testing.T, env protocol.Envelope) string {
	t.Helper()
	var we struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := env.Decode(&we); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var sd protocol.SessionDelta
	if err := json.Unmarshal(we.Data, &sd); err != nil {
		t.Fatalf("decode session delta: %v", err)
	}
	return sd.Text
}

func deltaEnvelope(text string) protocol.Envelope {
	return protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: protocol.EvSessionDelta,
		Data: protocol.SessionDelta{SessionID: "s1", Text: text},
	})
}

// TestEventBufferFIFOAcrossRestart pins the queue contract: push/next/remove
// is FIFO, sequence numbers continue past files on disk (order survives a
// restart), stale .tmp files from an interrupted push are discarded, and a
// corrupt entry is skipped and removed instead of wedging the queue.
func TestEventBufferFIFOAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "buf")

	buf, err := openEventBuffer(dir)
	if err != nil {
		t.Fatalf("open buffer: %v", err)
	}
	for _, text := range []string{"e1", "e2"} {
		if err := buf.Push(deltaEnvelope(text)); err != nil {
			t.Fatalf("push %s: %v", text, err)
		}
	}

	// A crashed push and a corrupt entry must not affect the good ones.
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000099.json.tmp"), []byte("{"), 0o644); err != nil {
		t.Fatalf("plant tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000050.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("plant corrupt: %v", err)
	}

	// Restart: reopen against the same dir, push more, expect strict FIFO over
	// everything that survived.
	buf, err = openEventBuffer(dir)
	if err != nil {
		t.Fatalf("reopen buffer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "00000000000000000099.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("stale .tmp not cleaned up: %v", err)
	}
	for _, text := range []string{"e3", "e4"} {
		if err := buf.Push(deltaEnvelope(text)); err != nil {
			t.Fatalf("push %s: %v", text, err)
		}
	}

	var got []string
	for {
		env, path, ok := buf.Next()
		if !ok {
			break
		}
		got = append(got, deltaText(t, env))
		if err := buf.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	want := []string{"e1", "e2", "e3", "e4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("drain order = %v, want %v", got, want)
	}
	if entries := buf.entries(); len(entries) != 0 {
		t.Fatalf("queue not empty after drain: %v", entries)
	}
}

// TestEventBufferAtLeastOnce: an envelope that is read but not removed (send
// never completed) is delivered again after a restart.
func TestEventBufferAtLeastOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "buf")
	buf, err := openEventBuffer(dir)
	if err != nil {
		t.Fatalf("open buffer: %v", err)
	}
	if err := buf.Push(deltaEnvelope("retry-me")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := buf.Push(deltaEnvelope("next")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if env, _, ok := buf.Next(); !ok || deltaText(t, env) != "retry-me" {
		t.Fatalf("first Next = %v/%v, want retry-me", env, ok)
	}

	buf, err = openEventBuffer(dir) // crash before Remove
	if err != nil {
		t.Fatalf("reopen buffer: %v", err)
	}
	env, path, ok := buf.Next()
	if !ok || deltaText(t, env) != "retry-me" {
		t.Fatalf("replay after crash = %v/%v, want retry-me", env, ok)
	}
	if err := buf.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if env, _, ok := buf.Next(); !ok || deltaText(t, env) != "next" {
		t.Fatalf("second = %v/%v, want next", env, ok)
	}
}

// TestNodeReplaysBufferedEventsAfterReconnect is the offline scenario end to
// end: the hub drops the first connection right after registration, the node
// buffers adapter events while unreachable, and the reconnected hub receives
// them in enqueue order before the queue empties.
func TestNodeReplaysBufferedEventsAfterReconnect(t *testing.T) {
	var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var conns atomic.Int64
	var mu sync.Mutex
	var received []string
	gotCh := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var reg protocol.Envelope
		if err := c.ReadJSON(&reg); err != nil || reg.Type != protocol.MsgRegisterRequest {
			return
		}
		resp := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "test-node"})
		if err := c.WriteJSON(resp); err != nil {
			return
		}
		if conns.Add(1) == 1 {
			return // hub "goes down" right after the first registration
		}
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgEvent {
				continue
			}
			mu.Lock()
			received = append(received, deltaText(t, env))
			n := len(received)
			mu.Unlock()
			if n >= 3 {
				select {
				case gotCh <- struct{}{}:
				default:
				}
			}
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	dir := filepath.Join(t.TempDir(), "buf")
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", dir)

	cfg := Config{
		Name:           "test-node",
		Host:           "test",
		AgentTypes:     []string{"fake"},
		DefaultAdapter: "fake",
		HubURL:         "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node",
		TokenFile:      filepath.Join(t.TempDir(), "node-token"),
	}
	n := New(cfg, nil)
	if n.buf == nil {
		t.Fatal("expected event buffering to be enabled via FLEETDECK_EVENT_BUFFER_DIR")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = n.Run(ctx) }()

	// Enqueue while the hub is unreachable: these must land on disk and be
	// replayed in order once the node reconnects (first backoff is 1s).
	for _, text := range []string{"e1", "e2", "e3"} {
		n.enqueue(deltaEnvelope(text))
	}

	waitSignal(t, gotCh, 10*time.Second, "buffered events replayed")

	mu.Lock()
	defer mu.Unlock()
	want := []string{"e1", "e2", "e3"}
	if strings.Join(received, ",") != strings.Join(want, ",") {
		t.Fatalf("hub received %v, want %v", received, want)
	}
	if entries := n.buf.entries(); len(entries) != 0 {
		t.Fatalf("buffer not drained after replay: %v", entries)
	}
}
