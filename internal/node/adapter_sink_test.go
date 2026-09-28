package node

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// TestConnSinkUsageEnvelope pins the node->hub framing of a usage report: a
// MsgEvent envelope carrying an EvSessionUsage payload whose counters are
// copied verbatim from the adapter's report. The hub persists that payload as
// a token_usage row (see hub.applyUsage).
func TestConnSinkUsageEnvelope(t *testing.T) {
	buf, err := openEventBuffer(t.TempDir())
	if err != nil {
		t.Fatalf("openEventBuffer: %v", err)
	}
	n := &Node{log: slog.Default(), buf: buf, sessions: map[string]adapters.Adapter{}}

	(&connSink{n: n}).Usage(context.Background(), "sess-1", adapters.Usage{
		Model:            "m",
		PromptTokens:     1,
		CompletionTokens: 2,
		TotalTokens:      3,
	})

	entries, err := os.ReadDir(buf.dir)
	if err != nil {
		t.Fatalf("read buffer dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("buffered envelopes = %d, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(buf.dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read buffered envelope: %v", err)
	}
	var frame struct {
		Type string `json:"type"`
		Data struct {
			Type string                `json:"type"`
			Data protocol.SessionUsage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if frame.Type != protocol.MsgEvent {
		t.Fatalf("envelope type = %q, want %q", frame.Type, protocol.MsgEvent)
	}
	if frame.Data.Type != protocol.EvSessionUsage {
		t.Fatalf("event type = %q, want %q", frame.Data.Type, protocol.EvSessionUsage)
	}
	want := protocol.SessionUsage{
		SessionID:        "sess-1",
		Model:            "m",
		PromptTokens:     1,
		CompletionTokens: 2,
		TotalTokens:      3,
	}
	if frame.Data.Data != want {
		t.Fatalf("payload = %+v, want %+v", frame.Data.Data, want)
	}
}
