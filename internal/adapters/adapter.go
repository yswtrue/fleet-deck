// Package adapters defines the contract every agent backend implements and a
// registry to look them up by agent type. Concrete adapters (fake, pi, ACP,
// codex) live in subpackages and register themselves into Registry.Default.
package adapters

import (
	"context"

	"fleetdeck/internal/protocol"
)

// StartRequest is the input to Adapter.Start.
type StartRequest struct {
	SessionID     string
	TaskID        string
	AgentType     string
	Prompt        string
	WorkspacePath string
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

	// SendMessage resumes an awaiting_user session with the user's reply.
	SendMessage(ctx context.Context, sessionID string, message string) error

	// Cancel terminates a running session.
	Cancel(ctx context.Context, sessionID string) error
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
