package node

import (
	"context"
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

func (s *connSink) TextDelta(_ context.Context, sessionID, text string) {
	s.emit(protocol.EvSessionDelta, protocol.SessionDelta{SessionID: sessionID, Text: text})
}

// Message reports a complete assistant message. It travels as its own
// MsgSessionEvent framing rather than being wrapped in a MsgEvent, because the
// hub persists it directly as a chat-history row.
func (s *connSink) Message(_ context.Context, sessionID string, msg protocol.Message) {
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
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: status})
}

func (s *connSink) UserInputRequested(_ context.Context, sessionID, prompt string) {
	s.emit(protocol.EvSessionUserInputRequest, protocol.SessionUserInputRequested{SessionID: sessionID, Prompt: prompt})
}

func (s *connSink) Done(_ context.Context, sessionID string) {
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: protocol.SessionDone})
	s.n.forget(sessionID)
}

func (s *connSink) Error(_ context.Context, sessionID string, err error) {
	s.n.log.Warn("adapter error", "session_id", sessionID, "err", err)
	s.emit(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: protocol.SessionFailed})
	s.n.forget(sessionID)
}

func (s *connSink) emit(eventType string, payload any) {
	s.n.enqueue(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: eventType,
		Data: payload,
		Ts:   time.Now().UTC(),
	}))
}
