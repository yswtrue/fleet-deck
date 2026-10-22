package hub

import (
	"testing"

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
