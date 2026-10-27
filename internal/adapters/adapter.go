// Package adapters defines the contract every agent backend implements and a
// registry to look them up by agent type. Concrete adapters (fake, pi, ACP,
// codex) live in subpackages and register themselves into Registry.Default.
package adapters

import (
	"context"
	"errors"

	"fleetdeck/internal/protocol"
)

// ErrResumeUnavailable reports that an adapter was armed to reattach to an
// agent-side session but the session could not be loaded (it was pruned, or
// the agent build cannot read it). The failure happens before the session is
// committed, so nothing was emitted to the sink: the node may start a fresh
// session instead — with the task's original prompt when the hub sent one as
// the retry fallback.
var ErrResumeUnavailable = errors.New("agent-side session cannot be reattached")

// StartRequest is the input to Adapter.Start.
type StartRequest struct {
	SessionID     string
	TaskID        string
	AgentType     string
	Prompt        string
	WorkspacePath string
	// AutoAllowScope is the owning workspace's permission auto-allow range.
	// Adapters that can answer permission asks automatically (the ACP adapter)
	// consult it; others ignore it. The zero value means "none" (always ask).
	AutoAllowScope protocol.AutoAllowScope
	// AutoTitle asks the adapter to obtain the agent's own session title when
	// the hub still treats the task title as provisional. Adapters whose agent
	// cannot report a title ignore it.
	AutoTitle bool
	// ResumeSessionID, when non-empty, asks the adapter to reattach to the
	// given agent-side session instead of starting a fresh one. Adapters
	// that can reattach (see Resumable) run Prompt as the first turn on the
	// reattached session; adapters that cannot ignore it and start fresh.
	ResumeSessionID string
}

// StartResult tells the caller what the adapter established.
type StartResult struct {
	ExternalSessionID string
}

// Usage is one turn's model token usage as reported by the agent. Adapters
// relay real numbers only; when an agent reports no usage, no Usage is
// emitted and nothing is recorded — numbers are never fabricated.
//
// The cache counters are pointers because ACP distinguishes "not reported"
// (omitted/null) from a reported zero: only agents whose model exposes prompt
// caching send them. PromptTokens is the non-cached input portion per ACP's
// end-turn usage semantics; the full input the model processed is
// PromptTokens + CachedReadTokens + CachedWriteTokens.
type Usage struct {
	Model             string
	PromptTokens      int64
	CompletionTokens  int64
	TotalTokens       int64
	CachedReadTokens  *int64
	CachedWriteTokens *int64
}

// Adapter is the contract every agent backend implements (pi-agent, ACP,
// codex, fake). The Node daemon drives the lifecycle; events flow out via the
// EventSink callback.
type Adapter interface {
	// Start launches a new session. Implementations return as soon as the
	// session is established and then drive events through the sink until
	// Done or Error. Must be safe to call once per Adapter instance.
	Start(ctx context.Context, req StartRequest, sink EventSink) (StartResult, error)

	// SendMessage resumes an awaiting_user session with the user's reply, or
	// starts the next turn of a running session after InterruptTurn (see
	// Steerer). Implementations must not run two turns at once.
	SendMessage(ctx context.Context, sessionID string, message string) error

	// Cancel terminates a running session. The terminal report is Error with
	// ErrStoppedByUser: a user stop is an interrupted task, not a finished one.
	Cancel(ctx context.Context, sessionID string) error
}

// ErrStoppedByUser is the terminal reason an adapter reports when Cancel
// stopped a session. The hub persists it as the session's failure reason.
var ErrStoppedByUser = errors.New("session stopped by user")

// SteerResult is what an interrupted turn left behind.
type SteerResult struct {
	// Active reports whether a turn was in flight (and therefore interrupted).
	// False means the session was between turns; SendMessage then starts one.
	Active bool
	// Text is the assistant text the interrupted turn produced. The adapter
	// does not emit it as a message: the node commits it to the transcript
	// ahead of the steering message, so the reader sees what the agent said
	// before the redirect (and the text is never duplicated).
	Text string
}

// Steerer is implemented by adapters that can stop the in-flight turn of a
// running session without ending it, so a user message can redirect the work.
// The node probes for it before SendMessage; adapters that do not implement it
// simply receive the message as the next turn.
type Steerer interface {
	// InterruptTurn cancels the turn in flight, waits for the agent to settle
	// it, and returns the text produced so far. It is a no-op (Active false)
	// when no turn is running.
	InterruptTurn(ctx context.Context, sessionID string) (SteerResult, error)
}

// Resumable is implemented by adapters that can reattach to an earlier
// agent-side session instead of starting a fresh one. The node probes for it
// when handling a session.resume command: adapters that do not implement it
// resume by starting a fresh session with the follow-up message.
type Resumable interface {
	// SetResumeSessionID arms the adapter so the next Start reattaches to the
	// agent-side session id instead of creating a new one. An empty id is a
	// no-op (Start behaves as usual). Must be called before Start.
	SetResumeSessionID(externalSessionID string)
}

// ThoughtSink is implemented by event sinks that can carry the agent's
// reasoning stream (ACP `agent_thought_chunk`). It is optional: sinks that do
// not implement it simply lose the trace, which is live-only and never
// persisted.
type ThoughtSink interface {
	ThoughtDelta(ctx context.Context, sessionID, text string)
}

// TitleSink is implemented by event sinks that can carry the title an agent
// assigned to its own session (ACP `session_info_update`). It is optional:
// sinks that do not implement it simply lose the title, and the task keeps its
// provisional prompt-derived label.
type TitleSink interface {
	Title(ctx context.Context, sessionID, title string)
}

// ToolCall is one tool invocation reported by an agent. ID is stable across
// the call's lifecycle updates (ACP toolCallId); Title/Kind/Status may arrive
// progressively, and an update with an empty field leaves the prior value
// untouched. Content is intentionally not copied: the title is the transcript
// line, and shipping raw tool input/output would leak file contents and
// command lines into the live view.
type ToolCall struct {
	ID     string
	Title  string
	Kind   string
	Status string
}

// ToolCallSink is implemented by event sinks that can carry the agent's tool
// calls (ACP `tool_call` / `tool_call_update`). It is optional: sinks that do
// not implement it lose the rows, and the assistant text alone remains.
type ToolCallSink interface {
	ToolCall(ctx context.Context, sessionID string, call ToolCall)
}

// PendingAsker is implemented by adapters that can pause on a user-input
// request (a permission pick or a form elicitation). The node re-emits the
// outstanding ask after a reconnect: a frame lost while the link was down
// would otherwise leave the session waiting for a reply the hub never saw.
type PendingAsker interface {
	// PendingAsk returns the prompt of the unanswered ask, and false when
	// nothing is outstanding.
	PendingAsk() (prompt string, ok bool)
}

// ReplayRequest is the input to HistoryReader.ReplaySession.
type ReplayRequest struct {
	SessionID         string
	ExternalSessionID string
	WorkspacePath     string
}

// HistoryReader is implemented by adapters that can replay a previous
// agent-side session's conversation without running it. It backs the hub's
// history proxy (privacy model: session content is stored only by the agent
// itself, so the server fetches it on demand through the node).
//
// ReplaySession must not send prompts or otherwise advance the session: it
// reads the recorded conversation and returns it oldest-first. It may spawn a
// short-lived agent process to do so. Implementations return the returned
// slice as protocol.Message rows with stable unique IDs and no fabricated
// timestamps (leave CreatedAt zero when the agent reports none).
type HistoryReader interface {
	ReplaySession(ctx context.Context, req ReplayRequest) ([]protocol.Message, error)
}

// EventSink is the outbound channel through which adapters report what
// happened. Implementations must be non-blocking; if the consumer is slow,
// the adapter should drop or coalesce (the hub reads from session events
// table anyway).
//
// TextDelta carries incremental streamed chunks; Message carries a complete
// chat-history line once a burst is finished.
type EventSink interface {
	TextDelta(ctx context.Context, sessionID string, text string)
	// Message reports a complete assistant message appended to the chat history.
	// Use this for terminal/structured output; use TextDelta for incremental
	// streamed chunks within a single message.
	Message(ctx context.Context, sessionID string, msg protocol.Message)
	TodoUpdate(ctx context.Context, sessionID string, todos []protocol.Todo)
	// StateChange reports a non-terminal transition (e.g. waiting -> running,
	// running -> awaiting_user). Terminal outcomes are Done and Error, which
	// already imply the final state — do NOT pair them with a terminal
	// StateChange, or the consumer sees the transition twice.
	StateChange(ctx context.Context, sessionID string, status protocol.SessionStatus)
	UserInputRequested(ctx context.Context, sessionID string, prompt string)
	// Usage reports one turn's model token usage. Emit it per turn when the
	// agent reports usage; omit it entirely when the agent reports none.
	Usage(ctx context.Context, sessionID string, u Usage)
	// Done ends the session successfully. It is terminal: implementations map
	// it to the session's final (done) state on the wire.
	Done(ctx context.Context, sessionID string)
	// Error ends the session with a failure. It is terminal: implementations
	// map it to the session's final (failed) state on the wire.
	Error(ctx context.Context, sessionID string, err error)
}
