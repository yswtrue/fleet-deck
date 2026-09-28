package fake

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// ---- recording sink -------------------------------------------------------

type recordedEvent struct {
	kind      string
	sessionID string
	text      string
	todos     []protocol.Todo
	status    protocol.SessionStatus
	prompt    string
	msg       protocol.Message
	usage     adapters.Usage
}

const (
	kindTextDelta   = "text_delta"
	kindMessage     = "message"
	kindTodoUpdate  = "todo_update"
	kindStateChange = "state_change"
	kindUsage       = "usage"
	kindUserInput   = "user_input_requested"
	kindDone        = "done"
	kindError       = "error"
)

type recorder struct {
	mu        sync.Mutex
	events    []recordedEvent
	done      chan struct{}
	doneOnce  sync.Once
	userInput chan string
}

func newRecorder() *recorder {
	return &recorder{
		done:      make(chan struct{}),
		userInput: make(chan string, 1),
	}
}

func (r *recorder) record(e recordedEvent) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) TextDelta(_ context.Context, sessionID, text string) {
	r.record(recordedEvent{kind: kindTextDelta, sessionID: sessionID, text: text})
}

func (r *recorder) Message(_ context.Context, sessionID string, msg protocol.Message) {
	r.record(recordedEvent{kind: kindMessage, sessionID: sessionID, msg: msg})
}

func (r *recorder) TodoUpdate(_ context.Context, sessionID string, todos []protocol.Todo) {
	cp := make([]protocol.Todo, len(todos))
	copy(cp, todos)
	r.record(recordedEvent{kind: kindTodoUpdate, sessionID: sessionID, todos: cp})
}

func (r *recorder) StateChange(_ context.Context, sessionID string, status protocol.SessionStatus) {
	r.record(recordedEvent{kind: kindStateChange, sessionID: sessionID, status: status})
}

func (r *recorder) UserInputRequested(_ context.Context, sessionID, prompt string) {
	r.record(recordedEvent{kind: kindUserInput, sessionID: sessionID, prompt: prompt})
	select {
	case r.userInput <- prompt:
	default:
	}
}

func (r *recorder) Usage(_ context.Context, sessionID string, u adapters.Usage) {
	r.record(recordedEvent{kind: kindUsage, sessionID: sessionID, usage: u})
}

func (r *recorder) Done(_ context.Context, sessionID string) {
	r.record(recordedEvent{kind: kindDone, sessionID: sessionID})
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *recorder) Error(_ context.Context, sessionID string, err error) {
	r.record(recordedEvent{kind: kindError, sessionID: sessionID, text: err.Error()})
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *recorder) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done")
	}
}

func (r *recorder) waitUserInput(t *testing.T) {
	t.Helper()
	select {
	case <-r.userInput:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UserInputRequested")
	}
}

func (r *recorder) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) kinds() []string {
	events := r.snapshot()
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.kind
	}
	return kinds
}

func assertKinds(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

// countKind returns how many recorded events have the given kind.
func countKind(events []recordedEvent, kind string) int {
	n := 0
	for _, e := range events {
		if e.kind == kind {
			n++
		}
	}
	return n
}

func newStarted(t *testing.T, scenario Scenario, interval time.Duration) (*Adapter, *recorder) {
	t.Helper()
	a := New()
	a.Scenario = scenario
	a.TickInterval = interval
	rec := newRecorder()
	_, err := a.Start(context.Background(), adapters.StartRequest{
		SessionID: "sess-1",
		TaskID:    "task-1",
		AgentType: "fake",
		Prompt:    "do the thing",
	}, rec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return a, rec
}

// ---- tests ----------------------------------------------------------------

func TestHappyPath(t *testing.T) {
	_, rec := newStarted(t, ScenarioHappyPath, time.Millisecond)
	rec.waitDone(t)

	assertKinds(t, rec.kinds(), []string{
		kindStateChange, // running
		kindTodoUpdate,  // snapshot(0): Research in_progress
		kindTodoUpdate,  // snapshot(1): Research done, Plan in_progress
		kindTextDelta,
		kindTextDelta,
		kindTextDelta,
		kindMessage,    // complete assistant message after the text burst
		kindTodoUpdate, // snapshot(2)
		kindTodoUpdate, // snapshot(3): all done
		kindUsage,      // deterministic synthetic usage, once per run
		kindDone,       // terminal; Done carries the final state (no paired StateChange)
	})

	events := rec.snapshot()
	if events[0].status != protocol.SessionRunning {
		t.Errorf("first state = %q, want running", events[0].status)
	}
	if events[len(events)-1].kind != kindDone {
		t.Errorf("terminal event kind = %q, want %q", events[len(events)-1].kind, kindDone)
	}

	// At least one complete assistant message must be emitted.
	if n := countKind(events, kindMessage); n < 1 {
		t.Errorf("message events = %d, want >= 1", n)
	}
	if events[6].msg.Role != protocol.RoleAssistant || events[6].msg.Content == "" {
		t.Errorf("message = %+v, want non-empty assistant message", events[6].msg)
	}

	// First todo snapshot: first item in progress, rest pending.
	first := events[1].todos
	if len(first) != 3 {
		t.Fatalf("todo count = %d, want 3", len(first))
	}
	if first[0].Status != protocol.SubtaskInProgress || first[1].Status != protocol.SubtaskPending || first[2].Status != protocol.SubtaskPending {
		t.Errorf("initial todos = %+v, want item 0 in_progress", first)
	}

	// Final todo snapshot: everything done.
	final := events[8].todos
	for i, todo := range final {
		if todo.Status != protocol.SubtaskDone {
			t.Errorf("final todo[%d] = %q, want done", i, todo.Status)
		}
	}
}

func TestAsksInput(t *testing.T) {
	a, rec := newStarted(t, ScenarioAsksInput, time.Millisecond)
	rec.waitUserInput(t)

	// Paused: no Done yet, and SendMessage is required to continue.
	kinds := rec.kinds()
	assertKinds(t, kinds, []string{
		kindStateChange, // running
		kindTodoUpdate,  // snapshot(0)
		kindTodoUpdate,  // snapshot(1)
		kindUserInput,
	})

	if err := a.SendMessage(context.Background(), "sess-1", "yes, proceed"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	rec.waitDone(t)

	assertKinds(t, rec.kinds(), []string{
		kindStateChange,
		kindTodoUpdate,
		kindTodoUpdate,
		kindUserInput,
		kindTextDelta,
		kindMessage, // complete assistant message after the text burst
		kindTodoUpdate,
		kindTodoUpdate,
		kindUsage, // deterministic synthetic usage, once per run
		kindDone,  // terminal; Done carries the final state (no paired StateChange)
	})

	events := rec.snapshot()
	if events[3].prompt != "Should I proceed?" {
		t.Errorf("prompt = %q, want %q", events[3].prompt, "Should I proceed?")
	}
	if n := countKind(events, kindMessage); n < 1 {
		t.Errorf("message events = %d, want >= 1", n)
	}
}

func TestSendMessageNotAwaitingUser(t *testing.T) {
	a, _ := newStarted(t, ScenarioHappyPath, time.Hour)
	defer func() {
		if err := a.Cancel(context.Background(), "sess-1"); err != nil {
			t.Errorf("Cancel cleanup: %v", err)
		}
	}()

	err := a.SendMessage(context.Background(), "sess-1", "hello")
	if !errors.Is(err, ErrNotAwaitingUser) {
		t.Fatalf("SendMessage error = %v, want ErrNotAwaitingUser", err)
	}
}

func TestSendMessageUnknownSession(t *testing.T) {
	a, _ := newStarted(t, ScenarioHappyPath, time.Hour)
	defer func() { _ = a.Cancel(context.Background(), "sess-1") }()

	if err := a.SendMessage(context.Background(), "nope", "hello"); err == nil {
		t.Fatal("SendMessage unknown session: got nil error, want error")
	}
}

func TestCancel(t *testing.T) {
	a, rec := newStarted(t, ScenarioHappyPath, time.Hour)
	defer func() { _ = a.Cancel(context.Background(), "sess-1") }()

	if err := a.Cancel(context.Background(), "sess-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	rec.waitDone(t)

	assertKinds(t, rec.kinds(), []string{
		kindStateChange, // running
		kindTodoUpdate,  // snapshot(0) emitted before first pause
		kindUsage,       // deterministic synthetic usage, once per run
		kindDone,        // terminal; Done carries the final state (no paired StateChange)
	})

	events := rec.snapshot()
	if events[3].kind != kindDone {
		t.Errorf("terminal event kind = %q, want %q", events[3].kind, kindDone)
	}
}

// TestSyntheticUsagePerRun pins the deterministic synthetic usage the fake
// reports so tests and the UI always have data: model "fake", 120 prompt +
// 80 completion = 200 total, exactly one report per run, before Done.
func TestSyntheticUsagePerRun(t *testing.T) {
	_, rec := newStarted(t, ScenarioHappyPath, time.Millisecond)
	rec.waitDone(t)

	events := rec.snapshot()
	usages := []recordedEvent{}
	for _, e := range events {
		if e.kind == kindUsage {
			usages = append(usages, e)
		}
	}
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1: %+v", len(usages), usages)
	}
	u := usages[0].usage
	if u != (adapters.Usage{Model: "fake", PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200}) {
		t.Fatalf("usage = %+v, want model fake / 120 / 80 / 200", u)
	}
	if events[len(events)-2].kind != kindUsage || events[len(events)-1].kind != kindDone {
		t.Fatalf("usage must precede terminal Done, got tail %q %q",
			events[len(events)-2].kind, events[len(events)-1].kind)
	}
}

func TestCancelUnknownSession(t *testing.T) {
	a, _ := newStarted(t, ScenarioHappyPath, time.Hour)
	defer func() { _ = a.Cancel(context.Background(), "sess-1") }()

	if err := a.Cancel(context.Background(), "nope"); err == nil {
		t.Fatal("Cancel unknown session: got nil error, want error")
	}
}

func TestStartTwice(t *testing.T) {
	a, _ := newStarted(t, ScenarioHappyPath, time.Hour)
	defer func() { _ = a.Cancel(context.Background(), "sess-1") }()

	_, err := a.Start(context.Background(), adapters.StartRequest{SessionID: "sess-2"}, newRecorder())
	if err == nil {
		t.Fatal("second Start: got nil error, want error")
	}
}

func TestRegistry(t *testing.T) {
	r := adapters.NewRegistry()

	if _, ok := r.Get("fake"); ok {
		t.Fatal("empty registry returned fake adapter")
	}

	r.Register("fake", func() adapters.Adapter { return New() })
	got, ok := r.Get("fake")
	if !ok {
		t.Fatal("Get(fake) not found after Register")
	}
	if _, isFake := got.(*Adapter); !isFake {
		t.Fatalf("Get(fake) = %T, want *fake.Adapter", got)
	}

	types := r.Types()
	if len(types) != 1 || types[0] != "fake" {
		t.Fatalf("Types() = %v, want [fake]", types)
	}
}

// The default registry is populated by this package's init().
func TestDefaultRegistryHasFake(t *testing.T) {
	if _, ok := adapters.Default.Get("fake"); !ok {
		t.Fatal("adapters.Default has no fake adapter registered via init()")
	}
}
