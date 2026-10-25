package node

import (
	"context"
	"strings"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// connSink is the EventSink the node injects into every adapter. Each callback
// is mapped onto an outbound hub message; every method is non-blocking,
// enqueueing onto the node's single writer buffer (see Node.enqueue).
//
// The hub is the only DB writer. This sink never touches Postgres: it merely
// reports what the adapter did, and the hub turns reports into rows and
// broadcasts.
type connSink struct {
	n *Node
}

var _ adapters.EventSink = (*connSink)(nil)
var _ adapters.TitleSink = (*connSink)(nil)
var _ adapters.ToolCallSink = (*connSink)(nil)

// Title reports the title the agent assigned to its session. It travels as a
// dedicated node message so the hub can map it onto the owning task.
func (s *connSink) Title(_ context.Context, sessionID, title string) {
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgSessionTitle, protocol.SessionTitle{
		SessionID: sessionID,
		Title:     title,
	}))
}

// ThoughtDelta reports a chunk of the agent's reasoning. It is kept in the
// node's in-memory transcript (bounded) so a page opened mid-turn can restore
// what the agent already reasoned; it never reaches Postgres.
func (s *connSink) ThoughtDelta(_ context.Context, sessionID, text string) {
	s.n.transcripts.appendThought(sessionID, text)
	s.emit(protocol.EvSessionThought, protocol.SessionThought{SessionID: sessionID, Text: text})
}

func (s *connSink) TextDelta(_ context.Context, sessionID, text string) {
	s.n.transcripts.appendDelta(sessionID, text)
	s.emit(protocol.EvSessionDelta, protocol.SessionDelta{SessionID: sessionID, Text: text})
}

// ToolCall records one tool invocation in the live transcript and reports the
// updated row to the hub as a complete message. Using the normal
// session.message framing means live clients, the reconnect gap-fill and a
// late browser attach all agree on the row's identity: the same id is updated
// in place rather than appended a second time.
func (s *connSink) ToolCall(_ context.Context, sessionID string, call adapters.ToolCall) {
	msg, ok := s.n.transcripts.upsertTool(sessionID, call)
	if !ok {
		return
	}
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: sessionID,
		Message:   msg,
	}))
}

// Message reports a complete assistant message. It travels as its own
// MsgSessionEvent framing rather than being wrapped in a MsgEvent, because the
// hub buffers it as session text (the ephemeral transcript buffer) and echoes
// it to web clients as a chat-history line.
func (s *connSink) Message(_ context.Context, sessionID string, msg protocol.Message) {
	msg = s.n.transcripts.addMessage(sessionID, msg)
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: sessionID,
		Message:   msg,
	}))
}

func (s *connSink) TodoUpdate(_ context.Context, sessionID string, todos []protocol.Todo) {
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgTodoUpdate, protocol.TodoUpdate{
		SessionID: sessionID,
		Todos:     todos,
	}))
}

func (s *connSink) StateChange(_ context.Context, sessionID string, status protocol.SessionStatus) {
	if status != protocol.SessionAwaitingUser {
		s.n.transcripts.setPending(sessionID, "")
	}
	if status == protocol.SessionDone || status == protocol.SessionFailed {
		s.n.transcripts.clearPartial(sessionID)
	}
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: status})
}

func (s *connSink) UserInputRequested(_ context.Context, sessionID, prompt string) {
	s.n.transcripts.setPending(sessionID, prompt)
	s.emit(protocol.EvSessionUserInputRequest, protocol.SessionUserInputRequested{SessionID: sessionID, Prompt: prompt})
}

// Usage reports one turn's token usage. It travels as a MsgEvent envelope
// like the other reports; the hub persists it as a token_usage row.
func (s *connSink) Usage(_ context.Context, sessionID string, u adapters.Usage) {
	s.emit(protocol.EvSessionUsage, protocol.SessionUsage{
		SessionID:         sessionID,
		Model:             u.Model,
		PromptTokens:      u.PromptTokens,
		CompletionTokens:  u.CompletionTokens,
		TotalTokens:       u.TotalTokens,
		CachedReadTokens:  u.CachedReadTokens,
		CachedWriteTokens: u.CachedWriteTokens,
	})
}

func (s *connSink) Done(_ context.Context, sessionID string) {
	s.n.transcripts.finishSession(sessionID)
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: protocol.SessionDone})
	s.n.forget(sessionID)
}

func (s *connSink) Error(_ context.Context, sessionID string, err error) {
	s.n.log.Warn("adapter error", "session_id", sessionID, "err", err)
	s.n.transcripts.finishSession(sessionID)
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{
		SessionID: sessionID,
		Status:    protocol.SessionFailed,
		Error:     failureReason(err),
	})
	s.n.forget(sessionID)
}

// maxFailureReason bounds the reason carried to the hub: an agent can dump a
// long stderr block into an error, and the reason is rendered inline in the
// UI (and persisted with the session row).
const maxFailureReason = 2000

// failureReason renders an adapter error for the operator, capped at
// maxFailureReason runes so a runaway stderr dump cannot bloat the row.
func failureReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if runes := []rune(msg); len(runes) > maxFailureReason {
		msg = string(runes[:maxFailureReason]) + "…"
	}
	return msg
}

func (s *connSink) emit(eventType string, payload any) {
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: eventType,
		Data: payload,
		Ts:   time.Now().UTC(),
	}))
}
