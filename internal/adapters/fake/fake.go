// Package fake provides an in-memory Adapter that plays a deterministic
// scenario. It is used for UI development and integration tests without
// touching a real agent: it emits todo updates, streamed text, state changes,
// and (optionally) a user-input request, all through the injected EventSink.
package fake

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// Scenario selects which canned lifecycle the fake adapter plays.
type Scenario string

const (
	// ScenarioHappyPath runs Research -> Plan -> Implement to completion
	// without ever pausing. This is the default.
	ScenarioHappyPath Scenario = "happy_path"
	// ScenarioAsksInput pauses midway with a UserInputRequested and waits for
	// SendMessage before completing.
	ScenarioAsksInput Scenario = "asks_input"
)

// ErrNotAwaitingUser is returned by SendMessage when the session is not paused
// on a UserInputRequested.
var ErrNotAwaitingUser = errors.New("fake: session is not awaiting user input")

// DefaultTickInterval is the delay between lifecycle steps when TickInterval
// is left unset.
const DefaultTickInterval = 250 * time.Millisecond

// todoTitles are the three subtasks every scenario walks through.
var todoTitles = []string{"Research", "Plan", "Implement"}

// Adapter is an in-memory Adapter implementation. One Adapter drives one
// session; construct a new one per session (adapters.Default does this for
// you).
type Adapter struct {
	// TickInterval is the delay between lifecycle steps. Zero uses
	// DefaultTickInterval; tests set a tiny value to collapse the timeline.
	TickInterval time.Duration
	// Scenario selects the lifecycle to play. Empty defaults to
	// ScenarioHappyPath.
	Scenario Scenario

	mu         sync.Mutex
	started    bool
	done       bool
	status     protocol.SessionStatus
	sessionID  string
	replyCh    chan string
	cancelCh   chan struct{}
	cancelAck  chan struct{}
	cancelOnce sync.Once
	finishOnce sync.Once

	// Queued prompts requested through adapters.Queueing. They run one per
	// turn after the active scenario ends, in arrival order.
	queued    []protocol.Message
	scenario  Scenario
	interval  time.Duration
	nextMsgID atomic.Int64
}

// maxQueuedPrompts bounds one session's pending-prompt queue.
const maxQueuedPrompts = 16

// New returns a fake Adapter with default settings. Tests use this (or
// Adapter{}) directly instead of relying on the package init() registration.
func New() *Adapter {
	return &Adapter{}
}

// init registers the fake adapter into the process-wide registry so the node
// daemon can resolve AgentType "fake" by importing this package for its side
// effects:
//
//	import _ "fleetdeck/internal/adapters/fake"
func init() {
	adapters.Default.Register("fake", func() adapters.Adapter { return New() })
}

// SetResumeSessionID implements adapters.Resumable. The fake has no real
// agent-side session to reattach to, so a resume replays the scenario from
// the top like a fresh run (deliberate parity choice, documented on
// adapters.Resumable).
func (a *Adapter) SetResumeSessionID(externalSessionID string) {}

// Start emits the running state, kicks off the scenario, and returns
// immediately with a synthetic external session id.
func (a *Adapter) Start(ctx context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return adapters.StartResult{}, fmt.Errorf("fake: adapter for session %q already started", a.sessionID)
	}
	a.started = true
	a.sessionID = req.SessionID
	a.status = protocol.SessionRunning
	a.replyCh = make(chan string, 1)
	a.cancelCh = make(chan struct{})
	a.cancelAck = make(chan struct{})
	a.done = false
	a.queued = nil
	a.nextMsgID.Store(0)
	scenario := a.Scenario
	if scenario == "" {
		scenario = ScenarioHappyPath
	}
	interval := a.TickInterval
	if interval <= 0 {
		interval = DefaultTickInterval
	}
	a.scenario = scenario
	a.interval = interval
	a.mu.Unlock()

	sink.StateChange(ctx, req.SessionID, protocol.SessionRunning)

	go a.run(ctx, req.SessionID, scenario, interval, sink)

	return adapters.StartResult{ExternalSessionID: "fake-" + req.SessionID}, nil
}

// SendMessage resumes an awaiting_user session with the user's reply, or
// accepts a steering message while the canned scenario runs (there is nothing
// to interrupt: the fake has no model). ErrNotAwaitingUser is returned once the
// run is finishing.
func (a *Adapter) SendMessage(ctx context.Context, sessionID string, message string) error {
	a.mu.Lock()
	if !a.started || a.sessionID != sessionID {
		a.mu.Unlock()
		return fmt.Errorf("fake: unknown session %q", sessionID)
	}
	switch a.status {
	case protocol.SessionRunning:
		a.mu.Unlock()
		return nil
	case protocol.SessionAwaitingUser:
	default:
		a.mu.Unlock()
		return ErrNotAwaitingUser
	}
	a.status = protocol.SessionRunning
	replyCh := a.replyCh
	a.mu.Unlock()

	select {
	case replyCh <- message:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.cancelCh:
		return context.Canceled
	}
	recordMessage(sessionID, protocol.Message{
		ID:        fmt.Sprintf("%s-user-%d", sessionID, time.Now().UnixNano()),
		SessionID: sessionID,
		Role:      protocol.RoleUser,
		Content:   message,
		CreatedAt: time.Now().UTC(),
	})
	return nil
}

// InterruptTurn implements adapters.Steerer. The fake has no in-flight model
// turn to interrupt, so it reports none and the node continues with the
// steering message.
func (a *Adapter) InterruptTurn(ctx context.Context, sessionID string) (adapters.SteerResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started || a.sessionID != sessionID {
		return adapters.SteerResult{}, fmt.Errorf("fake: unknown session %q", sessionID)
	}
	return adapters.SteerResult{}, nil
}

// QueueMessage implements adapters.Queueing: the prompt runs after the active
// scenario ends. The user row is emitted by endTurn when the prompt actually
// starts, never on receipt.
func (a *Adapter) QueueMessage(ctx context.Context, sessionID string, msg protocol.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started || a.sessionID != sessionID {
		return fmt.Errorf("fake: unknown session %q", sessionID)
	}
	if a.done {
		return errors.New("fake: session is finished")
	}
	if len(a.queued) >= maxQueuedPrompts {
		return errors.New("fake: prompt queue is full")
	}
	a.queued = append(a.queued, msg)
	return nil
}

// endTurn either starts the next queued prompt on a fresh scenario run or
// finishes the session. The user row is emitted before the queued run starts so
// the transcript order matches the wire.
func (a *Adapter) endTurn(ctx context.Context, sessionID string, sink adapters.EventSink) {
	a.mu.Lock()
	if len(a.queued) > 0 {
		msg := a.queued[0]
		a.queued = a.queued[1:]
		scenario, interval := a.scenario, a.interval
		a.mu.Unlock()
		sink.Message(ctx, sessionID, msg)
		go a.run(ctx, sessionID, scenario, interval, sink)
		return
	}
	a.done = true
	a.mu.Unlock()
	a.finish(ctx, sessionID, sink)
}

// Cancel stops the session and waits for it to report the terminal state. The
// run ends with Error(ErrStoppedByUser): a user stop is an interrupted task,
// not a completed one.
func (a *Adapter) Cancel(ctx context.Context, sessionID string) error {
	a.mu.Lock()
	if !a.started || a.sessionID != sessionID {
		a.mu.Unlock()
		return fmt.Errorf("fake: unknown session %q", sessionID)
	}
	alreadyDone := a.status == protocol.SessionDone || a.status == protocol.SessionFailed
	a.mu.Unlock()
	if alreadyDone {
		return nil
	}

	a.cancelOnce.Do(func() { close(a.cancelCh) })

	select {
	case <-a.cancelAck:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// run plays the selected scenario, emitting events until completion or
// cancellation.
func (a *Adapter) run(ctx context.Context, sessionID string, scenario Scenario, interval time.Duration, sink adapters.EventSink) {
	emitMessage := func(content string) {
		n := a.nextMsgID.Add(1)
		msg := protocol.Message{
			ID:        fmt.Sprintf("%s-msg-%d", sessionID, n),
			SessionID: sessionID,
			Role:      protocol.RoleAssistant,
			Content:   content,
			CreatedAt: time.Now().UTC(),
		}
		recordMessage(sessionID, msg)
		sink.Message(ctx, sessionID, msg)
	}

	sink.TodoUpdate(ctx, sessionID, snapshot(0))

	if !a.pause(ctx, interval) {
		a.endTurn(ctx, sessionID, sink)
		return
	}

	// First todo done, second in progress.
	sink.TodoUpdate(ctx, sessionID, snapshot(1))

	if scenario == ScenarioAsksInput {
		a.mu.Lock()
		a.status = protocol.SessionAwaitingUser
		a.mu.Unlock()
		sink.UserInputRequested(ctx, sessionID, "Should I proceed?")
		if !a.waitReply(ctx) {
			a.endTurn(ctx, sessionID, sink)
			return
		}
		sink.TextDelta(ctx, sessionID, "Thanks, proceeding as requested.")
		emitMessage("Thanks, proceeding as requested.")
		if !a.pause(ctx, interval) {
			a.endTurn(ctx, sessionID, sink)
			return
		}
		sink.TodoUpdate(ctx, sessionID, snapshot(2))
		if !a.pause(ctx, interval) {
			a.endTurn(ctx, sessionID, sink)
			return
		}
		sink.TodoUpdate(ctx, sessionID, snapshot(3))
		a.endTurn(ctx, sessionID, sink)
		return
	}

	sink.TextDelta(ctx, sessionID, "Research is done. ")
	sink.TextDelta(ctx, sessionID, "Now planning the next steps. ")
	sink.TextDelta(ctx, sessionID, "Implementing the change.")
	emitMessage("Research is done. Now planning the next steps. Implementing the change.")
	if !a.pause(ctx, interval) {
		a.endTurn(ctx, sessionID, sink)
		return
	}

	sink.TodoUpdate(ctx, sessionID, snapshot(2))
	if !a.pause(ctx, interval) {
		a.endTurn(ctx, sessionID, sink)
		return
	}

	sink.TodoUpdate(ctx, sessionID, snapshot(3))
	a.endTurn(ctx, sessionID, sink)
}

// pause sleeps for d, returning false if the context is done or the session
// was cancelled.
func (a *Adapter) pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-a.cancelCh:
		return false
	case <-t.C:
		return true
	}
}

// waitReply blocks until SendMessage delivers a reply, the context is done, or
// the session is cancelled.
func (a *Adapter) waitReply(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-a.cancelCh:
		return false
	case <-a.replyCh:
		return true
	}
}

// finish reports the terminal outcome exactly once, unless it was already
// cancelled into the same state. Done is terminal and carries the final
// (done) state itself (see EventSink); a separate StateChange would double it.
// Every run reports one deterministic synthetic usage sample first, so tests
// and the UI always have data.
func (a *Adapter) finish(ctx context.Context, sessionID string, sink adapters.EventSink) {
	a.finishOnce.Do(func() {
		sink.Usage(ctx, sessionID, adapters.Usage{
			Model:            "fake",
			PromptTokens:     120,
			CompletionTokens: 80,
			TotalTokens:      200,
		})
		stopped := false
		select {
		case <-a.cancelCh:
			stopped = true
		default:
		}
		a.mu.Lock()
		if stopped {
			a.status = protocol.SessionFailed
		} else {
			a.status = protocol.SessionDone
		}
		a.mu.Unlock()
		if stopped {
			sink.Error(ctx, sessionID, adapters.ErrStoppedByUser)
		} else {
			sink.Done(ctx, sessionID)
		}
		close(a.cancelAck)
	})
}

// snapshot builds the todo list where all items before active are done, the
// item at active is in progress, and the rest are pending. An active index
// past the end marks everything done.
func snapshot(active int) []protocol.Todo {
	todos := make([]protocol.Todo, len(todoTitles))
	for i, title := range todoTitles {
		status := protocol.SubtaskPending
		switch {
		case i < active:
			status = protocol.SubtaskDone
		case i == active:
			status = protocol.SubtaskInProgress
		}
		todos[i] = protocol.Todo{
			ID:     strconv.Itoa(i + 1),
			Title:  title,
			Status: status,
		}
	}
	return todos
}

// ---- history replay --------------------------------------------------------

var _ adapters.HistoryReader = (*Adapter)(nil)

// history records what this process's fake adapters emitted per session, so
// ReplaySession can serve it back. The fake has no agent-side session store;
// this in-memory log (daemon lifetime only) is the closest honest equivalent.
var (
	historyMu sync.Mutex
	history   = map[string][]protocol.Message{}
)

// recordMessage appends one message to a session's in-memory history.
func recordMessage(sessionID string, msg protocol.Message) {
	historyMu.Lock()
	defer historyMu.Unlock()
	history[sessionID] = append(history[sessionID], msg)
}

// ReplaySession implements adapters.HistoryReader: it returns the messages
// this daemon recorded for the session, oldest first. Sessions it never saw
// (e.g. across a daemon restart) report an error rather than an empty
// transcript — absence of history is not the same as an empty conversation.
func (a *Adapter) ReplaySession(_ context.Context, req adapters.ReplayRequest) ([]protocol.Message, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	msgs, ok := history[req.SessionID]
	if !ok {
		return nil, fmt.Errorf("fake: no recorded history for session %q", req.SessionID)
	}
	out := make([]protocol.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}
