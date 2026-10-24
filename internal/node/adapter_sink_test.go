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

// TestConnSinkToolCallEnvelope pins the node->hub framing of a tool call:
// even when an adapter reports it through the optional sink, it travels as a
// normal session.event so live clients and the transcript endpoint agree on
// the row's id.
func TestConnSinkToolCallEnvelope(t *testing.T) {
	buf, err := openEventBuffer(t.TempDir())
	if err != nil {
		t.Fatalf("openEventBuffer: %v", err)
	}
	n := &Node{log: slog.Default(), buf: buf, sessions: map[string]adapters.Adapter{}, transcripts: newTranscriptStore()}

	(&connSink{n: n}).ToolCall(context.Background(), "sess-1", adapters.ToolCall{
		ID:     "call-1",
		Title:  "Read README.md",
		Kind:   "read",
		Status: "completed",
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
		Type string                  `json:"type"`
		Data protocol.SessionMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if frame.Type != protocol.MsgSessionEvent {
		t.Fatalf("envelope type = %q, want %q", frame.Type, protocol.MsgSessionEvent)
	}
	msg := frame.Data.Message
	if msg.Role != protocol.RoleTool || msg.Tool == nil || msg.Tool.Title != "Read README.md" || msg.Tool.Status != "completed" {
		t.Fatalf("tool message = %+v", msg)
	}
	if msg.ID != "tool-sess-1-0-call-1" {
		t.Fatalf("tool id = %q", msg.ID)
	}
}

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

	read, write := int64(4), int64(5)
	(&connSink{n: n}).Usage(context.Background(), "sess-1", adapters.Usage{
		Model:             "m",
		PromptTokens:      1,
		CompletionTokens:  2,
		TotalTokens:       3,
		CachedReadTokens:  &read,
		CachedWriteTokens: &write,
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
	got := frame.Data.Data
	if got.SessionID != want.SessionID || got.Model != want.Model ||
		got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens ||
		got.TotalTokens != want.TotalTokens {
		t.Fatalf("payload = %+v, want %+v", got, want)
	}
	if got.CachedReadTokens == nil || *got.CachedReadTokens != read ||
		got.CachedWriteTokens == nil || *got.CachedWriteTokens != write {
		t.Fatalf("cache counters = %v/%v, want %d/%d",
			got.CachedReadTokens, got.CachedWriteTokens, read, write)
	}
}
