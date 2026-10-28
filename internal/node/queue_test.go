package node

import (
	"context"
	"sync"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// queueRecordingAdapter records the queued prompt handed to a Queueing adapter.
type queueRecordingAdapter struct {
	mu      sync.Mutex
	queued  []protocol.Message
	prompts []string
}

func (a *queueRecordingAdapter) Start(context.Context, adapters.StartRequest, adapters.EventSink) (adapters.StartResult, error) {
	return adapters.StartResult{}, nil
}

func (a *queueRecordingAdapter) SendMessage(_ context.Context, _ string, message string) error {
	a.mu.Lock()
	a.prompts = append(a.prompts, message)
	a.mu.Unlock()
	return nil
}

func (a *queueRecordingAdapter) Cancel(context.Context, string) error { return nil }

func (a *queueRecordingAdapter) QueueMessage(_ context.Context, _ string, msg protocol.Message) error {
	a.mu.Lock()
	a.queued = append(a.queued, msg)
	a.mu.Unlock()
	return nil
}

// A queue-mode message is handed to the adapter, which owns the turn boundary
// and emits the user row then. The node must not prompt immediately or echo a
// row ahead of the current turn's answer.
func TestQueueMessageGoesToAdapter(t *testing.T) {
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")
	reg := adapters.NewRegistry()
	adapter := &queueRecordingAdapter{}
	reg.Register("fake", func() adapters.Adapter { return adapter })

	n := New(Config{Name: "queue-test-node", DefaultAdapter: "fake"}, reg)
	n.out = make(chan protocol.Envelope, 8)
	n.outDone = make(chan struct{})
	n.sessions["sess-1"] = adapter

	msg := protocol.Message{
		ID:        "hub-user-1",
		SessionID: "sess-1",
		Role:      protocol.RoleUser,
		Content:   "run this after the turn",
		CreatedAt: time.Now().UTC(),
	}
	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSendMessage, protocol.SendMessageCommand{
		SessionID:   "sess-1",
		Message:     msg.Content,
		UserMessage: &msg,
		Queue:       true,
	}))

	adapter.mu.Lock()
	queued, prompts := adapter.queued, adapter.prompts
	adapter.mu.Unlock()
	if len(queued) != 1 || queued[0].ID != "hub-user-1" || queued[0].Content != msg.Content {
		t.Fatalf("queued = %+v, want the hub's message", queued)
	}
	if len(prompts) != 0 {
		t.Fatalf("SendMessage called immediately: %v", prompts)
	}
	select {
	case env := <-n.out:
		t.Fatalf("node emitted an envelope before the turn boundary: %+v", env)
	default:
	}
}
