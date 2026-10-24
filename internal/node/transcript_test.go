package node

import (
	"testing"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// The node cache is the live source of truth: prompt, assistant message and
// tool call keep arrival order, and the tool update mutates the same row.
func TestTranscriptKeepsOrderedToolRows(t *testing.T) {
	s := newTranscriptStore()
	s.beginRun("sess-1")
	s.addUser("sess-1", protocol.Message{ID: "u1", Role: protocol.RoleUser, Content: "do the thing"})
	s.appendDelta("sess-1", "working")
	created, ok := s.upsertTool("sess-1", adapters.ToolCall{ID: "call-1", Title: "Read file", Kind: "read", Status: "pending"})
	if !ok {
		t.Fatal("tool call was not recorded")
	}
	s.upsertTool("sess-1", adapters.ToolCall{ID: "call-1", Status: "completed"})
	s.addMessage("sess-1", protocol.Message{ID: "adapter-1", Role: protocol.RoleAssistant, Content: "done"})

	msgs, partial, pending := s.snapshot("sess-1")
	if partial != "" {
		t.Fatalf("partial = %q, want cleared by the complete message", partial)
	}
	if pending != "" {
		t.Fatalf("pending = %q, want empty", pending)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].ID != "u1" || msgs[0].Role != protocol.RoleUser {
		t.Fatalf("msgs[0] = %+v, want the hub-supplied prompt", msgs[0])
	}
	if msgs[1].Role != protocol.RoleTool || msgs[1].Tool == nil ||
		msgs[1].Tool.Title != "Read file" || msgs[1].Tool.Status != "completed" {
		t.Fatalf("msgs[1] = %+v, want the updated tool row", msgs[1])
	}
	if msgs[1].ID != created.ID {
		t.Fatalf("tool row id changed: %q -> %q", created.ID, msgs[1].ID)
	}
	if msgs[2].Role != protocol.RoleAssistant || msgs[2].Content != "done" {
		t.Fatalf("msgs[2] = %+v, want the final assistant message", msgs[2])
	}
	if msgs[2].ID != "msg-sess-1-1-1" {
		t.Fatalf("assistant id = %q, want a node-minted per-run id", msgs[2].ID)
	}
}

// Adapter message counters restart with each resume; the transcript ids must
// not, or the new run would overwrite the previous answer.
func TestTranscriptIDsAreUniqueAcrossRuns(t *testing.T) {
	s := newTranscriptStore()
	s.beginRun("sess-1")
	first := s.addMessage("sess-1", protocol.Message{Role: protocol.RoleAssistant, Content: "first"})
	s.beginRun("sess-1")
	second := s.addMessage("sess-1", protocol.Message{Role: protocol.RoleAssistant, Content: "second"})
	if first.ID == second.ID {
		t.Fatalf("resumed run reused id %q", first.ID)
	}
	msgs, _, _ := s.snapshot("sess-1")
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both runs kept: %+v", len(msgs), msgs)
	}
}

// A user-input ask and its partial survive in the cache for a late browser
// attach; any non-awaiting transition clears the ask.
func TestTranscriptPendingAndPartial(t *testing.T) {
	s := newTranscriptStore()
	s.setPending("sess-1", "Should I proceed?")
	s.appendDelta("sess-1", "half an answer")
	_, partial, pending := s.snapshot("sess-1")
	if partial != "half an answer" || pending != "Should I proceed?" {
		t.Fatalf("partial/pending = %q/%q", partial, pending)
	}
	s.setPending("sess-1", "")
	s.clearPartial("sess-1")
	_, partial, pending = s.snapshot("sess-1")
	if partial != "" || pending != "" {
		t.Fatalf("cleared partial/pending = %q/%q", partial, pending)
	}
}
