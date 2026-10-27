package node

import (
	"context"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// steerRecordingAdapter records the steering handshake and the prompts routed
// to it.
type steerRecordingAdapter struct {
	interrupted int
	prompts     []string
}

func (a *steerRecordingAdapter) Start(context.Context, adapters.StartRequest, adapters.EventSink) (adapters.StartResult, error) {
	return adapters.StartResult{}, nil
}

func (a *steerRecordingAdapter) SendMessage(_ context.Context, _ string, message string) error {
	a.prompts = append(a.prompts, message)
	return nil
}

func (a *steerRecordingAdapter) Cancel(context.Context, string) error { return nil }

func (a *steerRecordingAdapter) InterruptTurn(context.Context, string) (adapters.SteerResult, error) {
	a.interrupted++
	return adapters.SteerResult{Active: true, Text: "half an answer"}, nil
}

// A steering message must be committed after the interrupted turn's text: the
// adapter hands that text back instead of emitting it, so the node is the only
// place that can order the two rows. Both travel to the hub as session.message
// (the hub deliberately does not broadcast the steering row itself).
func TestSteerMessageOrdersInterruptedTextBeforeUserRow(t *testing.T) {
	// Keep the in-memory writer: the dev shell exports the disk buffer dir.
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")
	reg := adapters.NewRegistry()
	adapter := &steerRecordingAdapter{}
	reg.Register("fake", func() adapters.Adapter { return adapter })

	n := New(Config{Name: "steer-test-node", DefaultAdapter: "fake"}, reg)
	n.out = make(chan protocol.Envelope, 8)
	n.outDone = make(chan struct{})
	n.sessions["sess-1"] = adapter

	const prompt = "use the other API instead"
	msg := protocol.Message{
		ID:        "hub-user-1",
		SessionID: "sess-1",
		Role:      protocol.RoleUser,
		Content:   prompt,
		CreatedAt: time.Now().UTC(),
	}
	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSendMessage, protocol.SendMessageCommand{
		SessionID:   "sess-1",
		Message:     prompt,
		UserMessage: &msg,
		Steer:       true,
	}))

	if adapter.interrupted != 1 {
		t.Fatalf("InterruptTurn calls = %d, want 1", adapter.interrupted)
	}
	if len(adapter.prompts) != 1 || adapter.prompts[0] != prompt {
		t.Fatalf("prompts = %v, want [%q]", adapter.prompts, prompt)
	}

	interrupted := decodeSessionMessage(t, <-n.out)
	if interrupted.Message.Role != protocol.RoleAssistant || interrupted.Message.Content != "half an answer" {
		t.Fatalf("first row = %+v, want the interrupted assistant text", interrupted.Message)
	}
	steered := decodeSessionMessage(t, <-n.out)
	if steered.Message.Role != protocol.RoleUser || steered.Message.Content != prompt ||
		steered.Message.ID != "hub-user-1" {
		t.Fatalf("second row = %+v, want the hub's steering message", steered.Message)
	}

	// The transcript read (the authoritative source for the browser) agrees on
	// the order the live broadcast used.
	rows, _, _, _ := n.transcripts.snapshot("sess-1")
	if len(rows) != 2 || rows[0].Content != "half an answer" || rows[1].ID != "hub-user-1" {
		t.Fatalf("transcript rows = %+v, want interrupted text then the steer", rows)
	}
}

// A stop request for a session the node no longer runs must not leave the hub
// showing it as live: the node reports the failure itself.
func TestCancelWithoutAdapterFailsSession(t *testing.T) {
	// Keep the in-memory writer: the dev shell exports the disk buffer dir.
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")
	n := New(Config{Name: "cancel-test-node", DefaultAdapter: "fake"}, adapters.NewRegistry())
	n.out = make(chan protocol.Envelope, 8)
	n.outDone = make(chan struct{})

	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgCancel, protocol.CancelCommand{
		SessionID: "sess-gone",
	}))

	env := <-n.out
	if env.Type != protocol.MsgEvent {
		t.Fatalf("envelope type = %q, want %q", env.Type, protocol.MsgEvent)
	}
	var ev struct {
		Type string                       `json:"type"`
		Data protocol.SessionStateChanged `json:"data"`
	}
	if err := env.Decode(&ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	state := ev.Data
	if ev.Type != protocol.EvSessionStateChanged || state.SessionID != "sess-gone" ||
		state.Status != protocol.SessionFailed || state.Error == "" {
		t.Fatalf("event = %+v, want a failed state with a reason", ev)
	}
}

func decodeSessionMessage(t *testing.T, env protocol.Envelope) protocol.SessionMessage {
	t.Helper()
	if env.Type != protocol.MsgSessionEvent {
		t.Fatalf("envelope type = %q, want %q", env.Type, protocol.MsgSessionEvent)
	}
	var sm protocol.SessionMessage
	if err := env.Decode(&sm); err != nil {
		t.Fatalf("decode session message: %v", err)
	}
	return sm
}
