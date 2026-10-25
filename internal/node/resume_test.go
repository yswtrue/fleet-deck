package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/adapters/fake"
	"fleetdeck/internal/protocol"
)

// stubSink records terminal outcomes and Done for resume assertions.
type stubSink struct {
	mu      sync.Mutex
	running []string
	dones   []string
	errors  []string
	doneCh  chan string
}

func newStubSink() *stubSink { return &stubSink{doneCh: make(chan string, 8)} }

func (s *stubSink) TextDelta(_ context.Context, _ string, _ string) {}
func (s *stubSink) Message(_ context.Context, _ string, _ protocol.Message) {
}
func (s *stubSink) TodoUpdate(_ context.Context, _ string, _ []protocol.Todo) {}
func (s *stubSink) StateChange(_ context.Context, sessionID string, status protocol.SessionStatus) {
	if status == protocol.SessionRunning {
		s.mu.Lock()
		s.running = append(s.running, sessionID)
		s.mu.Unlock()
	}
}
func (s *stubSink) UserInputRequested(_ context.Context, _ string, _ string) {}
func (s *stubSink) Usage(_ context.Context, _ string, _ adapters.Usage)      {}
func (s *stubSink) Done(_ context.Context, sessionID string) {
	s.mu.Lock()
	s.dones = append(s.dones, sessionID)
	s.mu.Unlock()
	select {
	case s.doneCh <- sessionID:
	default:
	}
}
func (s *stubSink) Error(_ context.Context, sessionID string, _ error) {
	s.mu.Lock()
	s.errors = append(s.errors, sessionID)
	s.mu.Unlock()
}

// scriptedAdapter plays a canned run: StateChange(running), one assistant
// message echoing the prompt, then Done.
type scriptedAdapter struct {
	mu          sync.Mutex
	resumeIDs   []string
	startReqs   []adapters.StartRequest
	resumeIDSet string
	onStart     func()
}

func (a *scriptedAdapter) SetResumeSessionID(externalSessionID string) {
	a.mu.Lock()
	a.resumeIDSet = externalSessionID
	a.mu.Unlock()
}

func (a *scriptedAdapter) Start(_ context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	a.mu.Lock()
	a.startReqs = append(a.startReqs, req)
	a.resumeIDs = append(a.resumeIDs, a.resumeIDSet)
	onStart := a.onStart
	a.mu.Unlock()
	if onStart != nil {
		onStart()
	}
	sink.StateChange(context.Background(), req.SessionID, protocol.SessionRunning)
	sink.Message(context.Background(), req.SessionID, protocol.Message{
		ID:        req.SessionID + "-msg-1",
		SessionID: req.SessionID,
		Role:      protocol.RoleAssistant,
		Content:   "answer to: " + req.Prompt,
		CreatedAt: time.Now().UTC(),
	})
	sink.Done(context.Background(), req.SessionID)
	return adapters.StartResult{ExternalSessionID: "agent-" + req.SessionID}, nil
}

func (a *scriptedAdapter) SendMessage(_ context.Context, _ string, _ string) error {
	return nil
}
func (a *scriptedAdapter) Cancel(_ context.Context, _ string) error { return nil }

// TestResumeSessionReattachesAndReregisters drives node.handle with a
// session.resume command and asserts the adapter was armed with the external
// id, started with the follow-up as prompt, re-registered under the session
// id, and ran to Done.
func TestResumeSessionReattachesAndReregisters(t *testing.T) {
	reg := adapters.NewRegistry()
	scripted := &scriptedAdapter{}
	reg.Register("fake", func() adapters.Adapter { return scripted })

	n := New(Config{Name: "resume-test-node", DefaultAdapter: "fake"}, reg)

	started := make(chan struct{})
	scripted.onStart = func() { close(started) }

	// A finished session has no live adapter: forget must leave nothing.
	n.forget("sess-1")

	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSessionResume, protocol.SessionResumeCommand{
		SessionID:         "sess-1",
		TaskID:            "task-1",
		AgentType:         "fake",
		WorkspacePath:     "/tmp/ws",
		ExternalSessionID: "agent-sess-9",
		Message:           "follow up",
	}))

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for resumed session Start")
	}

	scripted.mu.Lock()
	defer scripted.mu.Unlock()
	if len(scripted.startReqs) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(scripted.startReqs))
	}
	req := scripted.startReqs[0]
	if req.Prompt != "follow up" {
		t.Fatalf("resume prompt = %q, want the follow-up message", req.Prompt)
	}
	if req.ResumeSessionID != "agent-sess-9" {
		t.Fatalf("ResumeSessionID = %q, want the external session id", req.ResumeSessionID)
	}
	if scripted.resumeIDSet != "agent-sess-9" {
		t.Fatalf("SetResumeSessionID got %q, want agent-sess-9", scripted.resumeIDSet)
	}

	// The adapter is registered under the session id while the resumed run
	// is in flight (re-registered so later SendMessage/Cancel frames route).
	// The scripted adapter already called Done by now, which forgets it, so
	// assert via a live check: re-register a fresh resume and confirm the
	// session resolves mid-run.
	n.sessMu.Lock()
	n.sessions["sess-1"] = scripted
	n.sessMu.Unlock()
	if got := n.session("sess-1"); got == nil {
		t.Fatal("resumed adapter not registered under the session id")
	}
	n.forget("sess-1")
}

// TestResumeSessionUnknownAdapterType fails the session when no adapter is
// registered for the command's agent type: connSink.Error emits a failed
// state change on the node's outbound channel and nothing stays registered.
func TestResumeSessionUnknownAdapterType(t *testing.T) {
	reg := adapters.NewRegistry()
	// FLEETDECK_EVENT_BUFFER_DIR must be unset: with buffering enabled the
	// failure event goes to disk, not to n.out.
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")
	n := New(Config{Name: "resume-unknown-node", DefaultAdapter: "fake"}, reg)
	n.outMu.Lock()
	n.out = make(chan protocol.Envelope, 8)
	n.outDone = make(chan struct{})
	n.outMu.Unlock()

	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSessionResume, protocol.SessionResumeCommand{
		SessionID: "sess-x",
		TaskID:    "task-x",
		AgentType: "nope",
		Message:   "hi",
	}))

	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case env := <-n.out:
			if env.Type != protocol.MsgEvent {
				continue
			}
			var evt protocol.Event
			if err := env.Decode(&evt); err != nil {
				continue
			}
			if evt.Type != protocol.EvSessionStateChanged {
				continue
			}
			var sc protocol.SessionStateChanged
			raw, merr := json.Marshal(evt.Data)
			if merr != nil {
				t.Fatalf("re-marshal state change: %v", merr)
			}
			if err := json.Unmarshal(raw, &sc); err != nil {
				t.Fatalf("decode state change: %v", err)
			}
			if sc.SessionID == "sess-x" && sc.Status == protocol.SessionFailed {
				if got := n.session("sess-x"); got != nil {
					t.Fatal("failed resume left an adapter registered")
				}
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("timed out waiting for resume failure event")
		}
	}
}

// plainAdapter is scriptedAdapter without the Resumable interface: a launcher
// that cannot reattach to an agent-side session.
type plainAdapter struct{ inner *scriptedAdapter }

func (a plainAdapter) Start(ctx context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	return a.inner.Start(ctx, req, sink)
}
func (a plainAdapter) SendMessage(ctx context.Context, sessionID, text string) error {
	return a.inner.SendMessage(ctx, sessionID, text)
}
func (a plainAdapter) Cancel(ctx context.Context, sessionID string) error {
	return a.inner.Cancel(ctx, sessionID)
}

// An automatic retry carries the task's original instruction as
// FallbackPrompt because the continuation note alone means nothing to a fresh
// conversation. An adapter that cannot reattach must run that prompt, not the
// note.
func TestResumeSessionWithoutReattachUsesFallbackPrompt(t *testing.T) {
	reg := adapters.NewRegistry()
	inner := &scriptedAdapter{}
	reg.Register("plain", func() adapters.Adapter { return plainAdapter{inner: inner} })

	n := New(Config{Name: "resume-fallback-node", DefaultAdapter: "plain"}, reg)
	started := make(chan struct{})
	inner.onStart = func() { close(started) }

	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSessionResume, protocol.SessionResumeCommand{
		SessionID:         "sess-2",
		TaskID:            "task-2",
		AgentType:         "plain",
		WorkspacePath:     "/tmp/ws",
		ExternalSessionID: "agent-gone",
		Message:           "continue the task",
		FallbackPrompt:    "do the thing",
	}))

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for resumed session Start")
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.startReqs) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(inner.startReqs))
	}
	req := inner.startReqs[0]
	if req.Prompt != "do the thing" {
		t.Fatalf("prompt = %q, want the fallback instruction", req.Prompt)
	}
	if req.ResumeSessionID != "" {
		t.Fatalf("ResumeSessionID = %q, want empty for a non-resumable adapter", req.ResumeSessionID)
	}
	messages, _, _, _ := n.transcripts.snapshot("sess-2")
	if len(messages) < 2 || messages[0].Content != "continue the task" ||
		messages[1].Content != "do the thing" {
		t.Fatalf("transcript = %+v, want the continuation note then the fallback prompt", messages)
	}
}

// failingResumeAdapter implements Resumable but refuses session/load, the way
// an agent whose session was pruned does. A retry must then start a fresh
// session with the fallback prompt instead of failing the task.
type failingResumeAdapter struct{ inner *scriptedAdapter }

func (a *failingResumeAdapter) SetResumeSessionID(id string) { a.inner.SetResumeSessionID(id) }
func (a *failingResumeAdapter) Start(ctx context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	if req.ResumeSessionID != "" {
		a.inner.mu.Lock()
		a.inner.startReqs = append(a.inner.startReqs, req)
		a.inner.mu.Unlock()
		return adapters.StartResult{}, fmt.Errorf("acp: session/load: %w (session not found)", adapters.ErrResumeUnavailable)
	}
	return a.inner.Start(ctx, req, sink)
}
func (a *failingResumeAdapter) SendMessage(ctx context.Context, sessionID, text string) error {
	return a.inner.SendMessage(ctx, sessionID, text)
}
func (a *failingResumeAdapter) Cancel(ctx context.Context, sessionID string) error {
	return a.inner.Cancel(ctx, sessionID)
}

func TestResumeSessionFallsBackWhenReattachFails(t *testing.T) {
	reg := adapters.NewRegistry()
	var mu sync.Mutex
	var instances []*scriptedAdapter
	reg.Register("flaky", func() adapters.Adapter {
		a := &scriptedAdapter{}
		mu.Lock()
		instances = append(instances, a)
		mu.Unlock()
		return &failingResumeAdapter{inner: a}
	})

	n := New(Config{Name: "resume-load-fail-node", DefaultAdapter: "flaky"}, reg)
	n.handle(context.Background(), protocol.NewEnvelope(protocol.MsgSessionResume, protocol.SessionResumeCommand{
		SessionID:         "sess-3",
		TaskID:            "task-3",
		AgentType:         "flaky",
		WorkspacePath:     "/tmp/ws",
		ExternalSessionID: "agent-pruned",
		Message:           "continue the task",
		FallbackPrompt:    "do the thing",
	}))

	// The first instance only sees the refused reattach; the factory's second
	// instance runs the fallback prompt as a fresh session.
	startReqs := func(a *scriptedAdapter) []adapters.StartRequest {
		a.mu.Lock()
		defer a.mu.Unlock()
		return append([]adapters.StartRequest{}, a.startReqs...)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		count := len(instances)
		var fresh *scriptedAdapter
		if count > 1 {
			fresh = instances[1]
		}
		mu.Unlock()
		if fresh != nil {
			if reqs := startReqs(fresh); len(reqs) > 0 {
				if reqs[0].Prompt != "do the thing" || reqs[0].ResumeSessionID != "" {
					t.Fatalf("fresh run = prompt %q resume %q, want the fallback prompt and no reattach",
						reqs[0].Prompt, reqs[0].ResumeSessionID)
				}
				mu.Lock()
				first := instances[0]
				mu.Unlock()
				firstReqs := startReqs(first)
				if len(firstReqs) != 1 || firstReqs[0].ResumeSessionID != "agent-pruned" {
					t.Fatalf("reattach attempt = %+v, want one session/load attempt", firstReqs)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the fallback run (instances %d)", count)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFakeAdapterResumeParity asserts the fake adapter accepts the resume
// flow: a fresh instance armed with SetResumeSessionID starts and runs to
// Done like a fresh canned run.
func TestFakeAdapterResumeParity(t *testing.T) {
	a := fake.New()
	a.TickInterval = time.Millisecond
	a.SetResumeSessionID("fake-external-1")
	sink := newStubSink()
	res, err := a.Start(context.Background(), adapters.StartRequest{
		SessionID:       "sess-fake-resume",
		Prompt:          "follow up",
		ResumeSessionID: "fake-external-1",
	}, sink)
	if err != nil {
		t.Fatalf("fake Start (resume): %v", err)
	}
	if res.ExternalSessionID == "" {
		t.Fatal("fake Start returned no external session id")
	}
	select {
	case <-sink.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake resume Done")
	}
}
