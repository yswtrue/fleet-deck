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
