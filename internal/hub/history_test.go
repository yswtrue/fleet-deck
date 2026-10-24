package hub

import (
	"testing"
	"time"

	"fleetdeck/internal/protocol"
)

// The agent-side replay can be poorer than the live buffer (pi-acp stores only
// the prompt while streaming a banner); history serves the union.
func TestMergeHistoryKeepsBufferedOnlyMessagesInPlace(t *testing.T) {
	replay := []protocol.Message{
		{ID: "r1", Role: protocol.RoleUser, Content: "prompt"},
		{ID: "r2", Role: protocol.RoleAssistant, Content: "answer"},
	}
	buffered := []protocol.Message{
		{ID: "b1", Role: protocol.RoleUser, Content: "prompt"},
		{ID: "b2", Role: protocol.RoleAssistant, Content: "banner"},
		{ID: "b3", Role: protocol.RoleThinking, Content: "thought"},
		{ID: "b4", Role: protocol.RoleAssistant, Content: "answer"},
	}
	got := mergeHistory(replay, buffered)
	if len(got) != 4 {
		t.Fatalf("merged = %d messages, want 4: %+v", len(got), got)
	}
	want := []struct {
		id      string
		content string
	}{
		{"r1", "prompt"},
		{"b2", "banner"},
		{"b3", "thought"},
		{"r2", "answer"},
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Content != w.content {
			t.Fatalf("merged[%d] = {%s,%q}, want {%s,%q}", i, got[i].ID, got[i].Content, w.id, w.content)
		}
	}
}

// A session that restarted agent-side (unresumable external id) replays only
// its new turns; the buffer's earlier turns must stay before them, not trail
// the transcript.
func TestMergeHistoryKeepsEarlierBufferedTurnsFirst(t *testing.T) {
	replay := []protocol.Message{
		{ID: "r2", Role: protocol.RoleUser, Content: "follow-up"},
		{ID: "r3", Role: protocol.RoleAssistant, Content: "new answer"},
	}
	buffered := []protocol.Message{
		{ID: "b1", Role: protocol.RoleUser, Content: "first prompt"},
		{ID: "b2", Role: protocol.RoleAssistant, Content: "first answer"},
		{ID: "b3", Role: protocol.RoleUser, Content: "follow-up"},
		{ID: "b4", Role: protocol.RoleAssistant, Content: "new answer"},
	}
	got := mergeHistory(replay, buffered)
	if len(got) != 4 {
		t.Fatalf("merged = %d messages, want 4: %+v", len(got), got)
	}
	want := []string{"first prompt", "first answer", "follow-up", "new answer"}
	for i, w := range want {
		if got[i].Content != w {
			t.Fatalf("merged[%d] = %q, want %q (order %+v)", i, got[i].Content, w, got)
		}
	}
}

// A buffered reply the node never received keeps its chronological position
// instead of trailing the node's newer rows, and a row present in both
// sources (same id) is emitted once.
func TestMergeLiveTranscriptsKeepsFallbackInOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	node := []protocol.Message{
		{ID: "u1", Role: protocol.RoleUser, Content: "prompt", CreatedAt: t0},
		{ID: "a1", Role: protocol.RoleAssistant, Content: "answer", CreatedAt: t0.Add(2 * time.Second)},
	}
	buffered := []protocol.Message{
		{ID: "u1", Role: protocol.RoleUser, Content: "prompt", CreatedAt: t0},
		{ID: "u2", Role: protocol.RoleUser, Content: "offline reply", CreatedAt: t0.Add(time.Second)},
	}
	got := mergeLiveTranscripts(node, buffered)
	if len(got) != 3 {
		t.Fatalf("merged = %d messages, want 3: %+v", len(got), got)
	}
	want := []string{"prompt", "offline reply", "answer"}
	for i, w := range want {
		if got[i].Content != w {
			t.Fatalf("merged[%d] = %q, want %q (order %+v)", i, got[i].Content, w, got)
		}
	}
}

// A limited read is the tail of the conversation: the web reloads a session to
// see its latest turns, and fleetdeck_message_tail asks for the most recent
// messages. Returning the head silently hides everything after the first page.
func TestLimitMessagesKeepsTheNewest(t *testing.T) {
	msgs := []protocol.Message{
		{ID: "m1", Role: protocol.RoleUser, Content: "one"},
		{ID: "m2", Role: protocol.RoleAssistant, Content: "two"},
		{ID: "m3", Role: protocol.RoleUser, Content: "three"},
		{ID: "m4", Role: protocol.RoleAssistant, Content: "four"},
	}
	got := limitMessages(msgs, 2)
	if len(got) != 2 || got[0].ID != "m3" || got[1].ID != "m4" {
		t.Fatalf("limit 2 = %+v, want the newest two in order", got)
	}
	if got := limitMessages(msgs, 10); len(got) != 4 {
		t.Fatalf("limit above the length = %d rows, want all 4", len(got))
	}
	// No limit means the whole transcript, and an empty source still marshals
	// as [] rather than null.
	if got := limitMessages(msgs, 0); len(got) != 4 {
		t.Fatalf("unlimited = %d rows, want all 4", len(got))
	}
	if got := limitMessages(nil, 0); got == nil || len(got) != 0 {
		t.Fatalf("empty transcript = %#v, want non-nil empty", got)
	}
}

// Repeated identical messages must not be deduped away and keep their order.
func TestMergeHistoryKeepsRepeatedMessages(t *testing.T) {
	replay := []protocol.Message{
		{ID: "r1", Role: protocol.RoleUser, Content: "yes"},
		{ID: "r2", Role: protocol.RoleAssistant, Content: "done"},
		{ID: "r3", Role: protocol.RoleUser, Content: "yes"},
	}
	buffered := []protocol.Message{
		{ID: "b1", Role: protocol.RoleUser, Content: "yes"},
		{ID: "b2", Role: protocol.RoleAssistant, Content: "done"},
	}
	got := mergeHistory(replay, buffered)
	if len(got) != 3 {
		t.Fatalf("merged = %d messages, want 3: %+v", len(got), got)
	}
}
