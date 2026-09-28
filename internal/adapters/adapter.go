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
}

// StartResult tells the caller what the adapter established.
type StartResult struct {
	ExternalSessionID string
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
	// Done ends the session successfully. It is terminal: implementations map
	// it to the session's final (done) state on the wire.
	Done(ctx context.Context, sessionID string)
	// Error ends the session with a failure. It is terminal: implementations
	// map it to the session's final (failed) state on the wire.
	Error(ctx context.Context, sessionID string, err error)
}
