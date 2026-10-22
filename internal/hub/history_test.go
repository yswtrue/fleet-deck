package hub

import (
	"testing"

	"fleetdeck/internal/protocol"
)

// The agent-side replay can be poorer than the live buffer (pi-acp stores only
// the prompt while streaming a banner); history serves the union.
func TestMergeHistoryAppendsBufferedOnlyMessages(t *testing.T) {
	replay := []protocol.Message{
		{ID: "r1", Role: protocol.RoleUser, Content: "prompt"},
		{ID: "r2", Role: protocol.RoleAssistant, Content: "answer"},
	}
	buffered := []protocol.Message{
		{ID: "b1", Role: protocol.RoleUser, Content: "prompt"},
		{ID: "b2", Role: protocol.RoleAssistant, Content: "banner"},
		{ID: "b3", Role: protocol.RoleThinking, Content: "thought"},
	}
	got := mergeHistory(replay, buffered)
	if len(got) != 4 {
		t.Fatalf("merged = %d messages, want 4: %+v", len(got), got)
	}
	if got[0].ID != "r1" || got[1].ID != "r2" {
		t.Fatalf("replay order changed: %+v", got[:2])
	}
	if got[2].Content != "banner" || got[3].Content != "thought" {
		t.Fatalf("buffered-only messages missing: %+v", got[2:])
	}
}
