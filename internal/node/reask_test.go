package node

import (
	"context"
	"encoding/json"
	"testing"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// pendingAdapter is an adapter paused on a user-input request.
type pendingAdapter struct{ prompt string }

func (p pendingAdapter) Start(context.Context, adapters.StartRequest, adapters.EventSink) (adapters.StartResult, error) {
	return adapters.StartResult{}, nil
}
func (p pendingAdapter) SendMessage(context.Context, string, string) error { return nil }
func (p pendingAdapter) Cancel(context.Context, string) error              { return nil }
func (p pendingAdapter) PendingAsk() (string, bool)                        { return p.prompt, p.prompt != "" }

// A reconnect must re-emit the ask: the hub only knows asks that arrived over
// the wire, so a frame lost while the link was down would leave the session
// waiting for a reply nobody can give.
func TestReaskPendingReemitsOutstandingAsk(t *testing.T) {
	// Keep the in-memory writer: the dev shell exports the disk buffer dir.
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")
	n := New(Config{Name: "test-node"}, adapters.NewRegistry())
	n.out = make(chan protocol.Envelope, 8)
	n.outDone = make(chan struct{})
	n.sessions = map[string]adapters.Adapter{
		"s-1": pendingAdapter{prompt: "Run shell command?\n\n[permission_request] options=[]"},
	}

	n.reaskPending(context.Background())

	if len(n.out) != 2 {
		t.Fatalf("emitted %d envelopes, want 2 (state + ask)", len(n.out))
	}
	decode := func(env protocol.Envelope) protocol.Event {
		t.Helper()
		if env.Type != protocol.MsgEvent {
			t.Fatalf("envelope type = %q, want %q", env.Type, protocol.MsgEvent)
		}
		var ev protocol.Event
		if err := env.Decode(&ev); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return ev
	}
	if ev := decode(<-n.out); ev.Type != protocol.EvSessionStateChanged {
		t.Fatalf("first event = %q, want %q", ev.Type, protocol.EvSessionStateChanged)
	}
	ev := decode(<-n.out)
	if ev.Type != protocol.EvSessionUserInputRequest {
		t.Fatalf("second event = %q, want %q", ev.Type, protocol.EvSessionUserInputRequest)
	}
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		t.Fatalf("re-encode ask payload: %v", err)
	}
	var u protocol.SessionUserInputRequested
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("decode ask payload: %v", err)
	}
	if u.SessionID != "s-1" || u.Prompt == "" {
		t.Fatalf("ask = %+v, want session s-1 with a prompt", u)
	}
}
