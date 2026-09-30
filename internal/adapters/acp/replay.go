package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

var _ adapters.HistoryReader = (*Adapter)(nil)

// ReplaySession implements adapters.HistoryReader: it spawns the agent,
// reattaches to the recorded agent-side session with `session/load` and no
// prompt (the load response arrives only after the agent has replayed the
// whole conversation — ACP session setup), collects the replayed messages
// through the replayAssembler, and reaps the process.
//
// It never advances the session: no prompt is sent and no live events are
// emitted (a discardSink swallows the adapter's own lifecycle signals).
func (a *Adapter) ReplaySession(ctx context.Context, req adapters.ReplayRequest) ([]protocol.Message, error) {
	if req.ExternalSessionID == "" {
		return nil, errors.New("acp: replay: no external session id")
	}
	if a.cfg.command == "" {
		return nil, fmt.Errorf("acp: no launch configuration for agent type %q", a.agentType)
	}

	asm := &replayAssembler{sessionID: req.SessionID}
	// Fixed for this one-shot adapter's lifetime: set before Start (which
	// spawns the read loop) and never cleared, so notification handling never
	// races with a later write.
	a.replayAsm = asm

	if _, err := a.Start(ctx, adapters.StartRequest{
		SessionID:       req.SessionID,
		ResumeSessionID: req.ExternalSessionID,
		WorkspacePath:   req.WorkspacePath,
	}, discardSink{}); err != nil {
		return nil, err
	}

	// The session/load response is ordered after every replayed chunk on the
	// single agent stream, so the assembler has seen the whole conversation.
	a.shutdown()
	return asm.messages(), nil
}

// replayAssembler rebuilds chat messages from a session/load replay stream.
// Agents replay history as `user_message_chunk` / `agent_message_chunk`
// updates tagged with an opaque messageId; consecutive chunks with the same id
// and role form one message. Without message ids only role changes mark
// boundaries (matching the ACP contract: ids are optional).
//
// Replayed messages carry no timestamps: the replay stream has none, and
// timestamps are never fabricated.
type replayAssembler struct {
	sessionID string
	msgs      []protocol.Message
	curID     string
	curRole   protocol.MessageRole
	cur       strings.Builder
}

// chunk appends one replayed text chunk, closing the open message when the
// message id or role changes.
func (r *replayAssembler) chunk(id string, role protocol.MessageRole, text string) {
	if id != r.curID || role != r.curRole {
		r.flush()
		r.curID, r.curRole = id, role
	}
	r.cur.WriteString(text)
}

// flush closes the open message (if any) and appends it to the list.
func (r *replayAssembler) flush() {
	content := r.cur.String()
	r.cur.Reset()
	if strings.TrimSpace(content) == "" {
		return
	}
	msgID := r.curID
	if msgID == "" {
		msgID = fmt.Sprintf("%s-replay-%d", r.sessionID, len(r.msgs))
	}
	r.msgs = append(r.msgs, protocol.Message{
		ID:        msgID,
		SessionID: r.sessionID,
		Role:      r.curRole,
		Content:   content,
	})
}

// messages returns the full replayed conversation, oldest first.
func (r *replayAssembler) messages() []protocol.Message {
	r.flush()
	if r.msgs == nil {
		return []protocol.Message{}
	}
	return r.msgs
}

// discardSink swallows the adapter's EventSink traffic during a replay run:
// history is collected through the replayAssembler, and a replay must not
// emit live session events (it never runs the session).
type discardSink struct{}

func (discardSink) TextDelta(context.Context, string, string)                   {}
func (discardSink) Message(context.Context, string, protocol.Message)           {}
func (discardSink) TodoUpdate(context.Context, string, []protocol.Todo)         {}
func (discardSink) StateChange(context.Context, string, protocol.SessionStatus) {}
func (discardSink) UserInputRequested(context.Context, string, string)          {}
func (discardSink) Usage(context.Context, string, adapters.Usage)               {}
func (discardSink) Done(context.Context, string)                                {}
func (discardSink) Error(context.Context, string, error)                        {}
