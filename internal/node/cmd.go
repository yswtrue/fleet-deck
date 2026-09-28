package node

import (
	"context"
	"fmt"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// handle dispatches one inbound hub message. It returns nothing: protocol
// errors are logged and the session is failed where appropriate.
func (n *Node) handle(ctx context.Context, env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgTaskStart:
		var cmd protocol.TaskStartCommand
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad task.start payload", "err", err)
			return
		}
		n.startSession(ctx, cmd)

	case protocol.MsgSendMessage:
		var cmd protocol.SendMessageCommand
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad session.send_message payload", "err", err)
			return
		}
		adapter := n.session(cmd.SessionID)
		if adapter == nil {
			n.log.Warn("send_message for unknown session", "session_id", cmd.SessionID)
			return
		}
		if err := adapter.SendMessage(ctx, cmd.SessionID, cmd.Message); err != nil {
			n.log.Warn("send_message failed", "session_id", cmd.SessionID, "err", err)
		}

	case protocol.MsgSessionResume:
		var cmd protocol.SessionResumeCommand
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad session.resume payload", "err", err)
			return
		}
		n.resumeSession(ctx, cmd)

	case protocol.MsgCancel:
		var cmd protocol.CancelCommand
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad session.cancel payload", "err", err)
			return
		}
		adapter := n.session(cmd.SessionID)
		if adapter == nil {
			n.log.Warn("cancel for unknown session", "session_id", cmd.SessionID)
			return
		}
		if err := adapter.Cancel(ctx, cmd.SessionID); err != nil {
			n.log.Warn("cancel failed", "session_id", cmd.SessionID, "err", err)
		}
		n.forget(cmd.SessionID)

	default:
		n.log.Debug("ignoring unknown hub message", "type", env.Type)
	}
}

// startSession resolves the adapter for the command's agent type, records it so
// later SendMessage/Cancel reach the same instance, and launches it.
func (n *Node) startSession(ctx context.Context, cmd protocol.TaskStartCommand) {
	agentType := cmd.AgentType
	if agentType == "" {
		agentType = n.cfg.DefaultAdapter
	}

	sink := &connSink{n: n}
	adapter, ok := n.registry.Get(agentType)
	if !ok {
		sink.Error(ctx, cmd.SessionID, fmt.Errorf("no adapter registered for agent_type %q", agentType))
		return
	}

	n.sessMu.Lock()
	n.sessions[cmd.SessionID] = adapter
	n.sessMu.Unlock()

	log := n.log.With("session_id", cmd.SessionID, "task_id", cmd.TaskID, "agent_type", agentType)
	log.Info("starting session")

	go func() {
		result, err := adapter.Start(ctx, adapters.StartRequest{
			SessionID:     cmd.SessionID,
			TaskID:        cmd.TaskID,
			AgentType:     agentType,
			Prompt:        cmd.Prompt,
			WorkspacePath: cmd.WorkspacePath,
		}, sink)
		if err != nil {
			log.Error("adapter start failed", "err", err)
			sink.Error(ctx, cmd.SessionID, err)
			n.forget(cmd.SessionID)
			return
		}
		log.Info("session started", "external_session_id", result.ExternalSessionID)
	}()
}

func (n *Node) session(sessionID string) adapters.Adapter {
	n.sessMu.Lock()
	defer n.sessMu.Unlock()
	return n.sessions[sessionID]
}

func (n *Node) forget(sessionID string) {
	n.sessMu.Lock()
	delete(n.sessions, sessionID)
	n.sessMu.Unlock()
}

// resumeSession revives a finished session from a session.resume command: it
// builds a fresh adapter from the registry, arms it with the recorded
// agent-side session id when the adapter supports reattach (adapters.Resumable
// — the ACP adapter's session/load path; others resume as a fresh session),
// and starts it with the user's follow-up as the first prompt. The adapter is
// re-registered under the session id so later SendMessage/Cancel frames route
// to it (connSink.forget removed the old instance after Done/Error).
//
// Unresumable sessions (no external id, or an adapter that cannot reattach)
// degrade to a fresh session seeded with the task prompt plus the follow-up:
// no error is reported to the hub beyond the normal event stream, and the
// session runs to a fresh Done/Error. Rationale: the user's message is already
// durable hub-side, and a follow-up deserves an answer even when the
// agent-side session is gone.
func (n *Node) resumeSession(ctx context.Context, cmd protocol.SessionResumeCommand) {
	agentType := cmd.AgentType
	if agentType == "" {
		agentType = n.cfg.DefaultAdapter
	}

	sink := &connSink{n: n}
	adapter, ok := n.registry.Get(agentType)
	if !ok {
		sink.Error(ctx, cmd.SessionID, fmt.Errorf("no adapter registered for agent_type %q", agentType))
		return
	}

	prompt := cmd.Message
	if cmd.ExternalSessionID != "" {
		if r, ok := adapter.(adapters.Resumable); ok {
			r.SetResumeSessionID(cmd.ExternalSessionID)
		} else {
			n.log.Info("adapter cannot reattach; resuming as a fresh session",
				"session_id", cmd.SessionID, "agent_type", agentType)
		}
	} else {
		n.log.Info("no external session id; resuming as a fresh session",
			"session_id", cmd.SessionID, "agent_type", agentType)
	}

	n.sessMu.Lock()
	n.sessions[cmd.SessionID] = adapter
	n.sessMu.Unlock()

	log := n.log.With("session_id", cmd.SessionID, "task_id", cmd.TaskID,
		"agent_type", agentType, "resume_external_id", cmd.ExternalSessionID)
	log.Info("resuming session")

	go func() {
		result, err := adapter.Start(ctx, adapters.StartRequest{
			SessionID:       cmd.SessionID,
			TaskID:          cmd.TaskID,
			AgentType:       agentType,
			Prompt:          prompt,
			WorkspacePath:   cmd.WorkspacePath,
			ResumeSessionID: cmd.ExternalSessionID,
		}, sink)
		if err != nil {
			log.Error("adapter resume failed", "err", err)
			sink.Error(ctx, cmd.SessionID, err)
			n.forget(cmd.SessionID)
			return
		}
		log.Info("session resumed", "external_session_id", result.ExternalSessionID)
	}()
}
