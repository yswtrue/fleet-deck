package acp

import (
	"context"
	"encoding/json"
	"testing"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// toolUpdate builds one ACP session/update notification for a tool call.
func toolUpdate(update map[string]any) inbound {
	params, _ := json.Marshal(map[string]any{
		"sessionId": "sess-1",
		"update":    update,
	})
	return inbound{JSONRPC: "2.0", Method: "session/update", Params: params}
}

// A tool call reaches the node sink with its stable id, human title, kind and
// status; the follow-up update carries the changed status (and no title), which
// the node merges into the same row.
func TestToolCallNotificationsReachSink(t *testing.T) {
	rec := newRecorder()
	a := &Adapter{}
	ctx := context.Background()

	a.handleNotification(ctx, "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "call-1",
		"title":         "Read README.md",
		"kind":          "read",
		"status":        "pending",
		// ACP tool content is an array. Decoding it into the chunk struct used
		// to fail the whole notification, so tool calls carrying content were
		// dropped before they could be displayed.
		"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "# FleetDeck"}}},
	}))
	first := <-rec.toolCh
	if first.ID != "call-1" || first.Title != "Read README.md" || first.Kind != "read" || first.Status != "pending" {
		t.Fatalf("first tool call = %+v", first)
	}

	a.handleNotification(ctx, "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "call-1",
		"status":        "completed",
	}))
	second := <-rec.toolCh
	if second.ID != "call-1" || second.Title != "" || second.Status != "completed" {
		t.Fatalf("tool update = %+v, want id call-1, empty title, completed", second)
	}
}

// Text produced before a tool call is flushed as its own assistant message
// before the tool row; text after the tool starts a new segment. Without this
// split the whole turn's text would be emitted after the tool and the
// transcript order would be wrong.
func TestToolCallSplitsStreamedTextSegments(t *testing.T) {
	rec := newRecorder()
	a := &Adapter{}
	ctx := context.Background()

	a.handleNotification(ctx, "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     "m1",
		"content":       map[string]any{"type": "text", "text": "checking the file"},
	}))
	a.handleNotification(ctx, "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "call-1",
		"title":         "Read README.md",
		"status":        "pending",
	}))
	a.handleNotification(ctx, "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     "m2",
		"content":       map[string]any{"type": "text", "text": "it is small"},
	}))
	if !a.flushAssistantText(ctx, rec) {
		t.Fatal("final text segment was not flushed")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	wantKinds := []string{"text_delta", "message", "tool_call", "text_delta", "message"}
	if len(rec.events) != len(wantKinds) {
		t.Fatalf("events = %+v, want %d", rec.events, len(wantKinds))
	}
	for i, kind := range wantKinds {
		if rec.events[i].kind != kind {
			t.Fatalf("events[%d] = %+v, want kind %q", i, rec.events[i], kind)
		}
	}
	if rec.events[1].content != "checking the file" || rec.events[4].content != "it is small" {
		t.Fatalf("segment contents = %q / %q", rec.events[1].content, rec.events[4].content)
	}
	if rec.events[2].text != "Read README.md" {
		t.Fatalf("tool title = %q", rec.events[2].text)
	}
}

// A tool call without an id cannot be updated in place; emitting per-frame
// rows would duplicate the tool in the transcript, so it is dropped.
func TestToolCallWithoutIDIsDropped(t *testing.T) {
	rec := newRecorder()
	a := &Adapter{}
	a.handleNotification(context.Background(), "sess-1", rec, toolUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"title":         "Read README.md",
		"status":        "pending",
	}))
	select {
	case call := <-rec.toolCh:
		t.Fatalf("tool call without id was emitted: %+v", call)
	default:
	}
}

// A session/load replay puts tool rows between the text that preceded and
// followed them, and updates replace rather than duplicate the row.
func TestReplayAssemblerInterleavesToolCalls(t *testing.T) {
	asm := &replayAssembler{sessionID: "sess-1"}
	asm.chunk("m1", protocol.RoleAssistant, "checking the file")
	asm.tool(adapters.ToolCall{ID: "call-1", Title: "Read README.md", Status: "pending"})
	asm.tool(adapters.ToolCall{ID: "call-1", Status: "completed"})
	asm.chunk("m2", protocol.RoleAssistant, "it is small")

	msgs := asm.messages()
	if len(msgs) != 3 {
		t.Fatalf("replay = %d messages, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != protocol.RoleAssistant || msgs[1].Role != protocol.RoleTool || msgs[2].Role != protocol.RoleAssistant {
		t.Fatalf("roles = %v/%v/%v, want assistant/tool/assistant", msgs[0].Role, msgs[1].Role, msgs[2].Role)
	}
	if msgs[1].Tool == nil || msgs[1].Tool.Title != "Read README.md" || msgs[1].Tool.Status != "completed" {
		t.Fatalf("tool row = %+v, want updated title/status", msgs[1].Tool)
	}
}
