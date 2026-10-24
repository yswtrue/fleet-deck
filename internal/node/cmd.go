package node

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		// Record the reply before the adapter can produce its answer, so the
		// transcript order is prompt then answer even on a late browser attach.
		if cmd.UserMessage != nil {
			n.transcripts.addUser(cmd.SessionID, *cmd.UserMessage)
		} else {
			n.transcripts.addUser(cmd.SessionID, protocol.Message{
				SessionID: cmd.SessionID,
				Role:      protocol.RoleUser,
				Content:   cmd.Message,
			})
		}
		// The reply answers the outstanding ask.
		n.transcripts.setPending(cmd.SessionID, "")
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

	case protocol.MsgSessionHistory:
		var cmd protocol.SessionHistoryRequest
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad session.history_req payload", "err", err)
			return
		}
		// Replay spawns an agent process and can take a while; never block the
		// hub-message read loop on it.
		go n.replayHistory(cmd)

	case protocol.MsgSessionTranscript:
		var cmd protocol.SessionTranscriptRequest
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad session.transcript_req payload", "err", err)
			return
		}
		n.sendTranscript(cmd)

	case protocol.MsgFSList:
		var cmd protocol.FSListRequest
		if err := env.Decode(&cmd); err != nil {
			n.log.Warn("bad fs.list_req payload", "err", err)
			return
		}
		// A slow file server (network mount) must not stall adapter traffic.
		go n.listDir(cmd)

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

	// The prompt opens the node-local transcript. The hub normally sends its
	// recorded copy (so the id matches the live broadcast); an older hub only
	// sends the text, in which case the node mints an id.
	n.transcripts.beginRun(cmd.SessionID)
	if cmd.UserMessage != nil {
		n.transcripts.addUser(cmd.SessionID, *cmd.UserMessage)
	} else if strings.TrimSpace(cmd.Prompt) != "" {
		n.transcripts.addUser(cmd.SessionID, protocol.Message{
			SessionID: cmd.SessionID,
			Role:      protocol.RoleUser,
			Content:   cmd.Prompt,
		})
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
		n.reportExternalSessionID(cmd.SessionID, result.ExternalSessionID)
	}()
}

// reportExternalSessionID tells the hub which agent-side session id the
// adapter opened (or reattached). Without it the hub cannot replay the
// conversation after the session finishes: GET /api/sessions/{id}/messages
// answers 503 once content is no longer in the in-memory text buffer.
func (n *Node) reportExternalSessionID(sessionID, externalID string) {
	if externalID == "" {
		return
	}
	n.enqueue(protocol.NewEnvelope(protocol.MsgSessionStarted, protocol.SessionStarted{
		SessionID:         sessionID,
		ExternalSessionID: externalID,
	}))
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

	// A resume continues the same transcript; open a new adapter generation so
	// adapter-side tool/message id counters do not collide with the prior run.
	n.transcripts.beginRun(cmd.SessionID)
	if cmd.UserMessage != nil {
		n.transcripts.addUser(cmd.SessionID, *cmd.UserMessage)
	} else if strings.TrimSpace(cmd.Message) != "" {
		n.transcripts.addUser(cmd.SessionID, protocol.Message{
			SessionID: cmd.SessionID,
			Role:      protocol.RoleUser,
			Content:   cmd.Message,
		})
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
		n.reportExternalSessionID(cmd.SessionID, result.ExternalSessionID)
	}()
}

// sendTranscript answers a session.transcript_req from the node-local cache.
// It is memory-only and cheap, so it runs synchronously; the hub uses it as
// the authoritative source for a live session's message order.
func (n *Node) sendTranscript(cmd protocol.SessionTranscriptRequest) {
	messages, partial, thought, pending := n.transcripts.snapshot(cmd.SessionID)
	n.enqueue(protocol.NewEnvelope(protocol.MsgSessionTranscriptResp, protocol.SessionTranscriptResponse{
		ReqID:          cmd.ReqID,
		SessionID:      cmd.SessionID,
		Messages:       messages,
		PartialOutput:  partial,
		PartialThought: thought,
		PendingInput:   pending,
	}))
}

// replayTimeout bounds one history replay: it may spawn a short-lived agent
// process, so it gets a generous but finite budget.
const replayTimeout = 60 * time.Second

// replayHistory answers a session.history_req: it replays the agent-side
// session's conversation through a fresh adapter (replay is read-only and
// independent of any live session instance) and reports the messages back to
// the hub. Failures travel in the response payload — the hub then falls back
// to its ephemeral text buffer.
func (n *Node) replayHistory(cmd protocol.SessionHistoryRequest) {
	resp := protocol.SessionHistoryResponse{
		ReqID:     cmd.ReqID,
		SessionID: cmd.SessionID,
		Messages:  []protocol.Message{},
	}
	send := func() { n.enqueue(protocol.NewEnvelope(protocol.MsgSessionHistoryResp, resp)) }

	agentType := cmd.AgentType
	if agentType == "" {
		agentType = n.cfg.DefaultAdapter
	}
	adapter, ok := n.registry.Get(agentType)
	if !ok {
		resp.Error = fmt.Sprintf("no adapter registered for agent_type %q", agentType)
		send()
		return
	}
	reader, ok := adapter.(adapters.HistoryReader)
	if !ok {
		resp.Error = fmt.Sprintf("agent_type %q has no history replay support", agentType)
		send()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()
	msgs, err := reader.ReplaySession(ctx, adapters.ReplayRequest{
		SessionID:         cmd.SessionID,
		ExternalSessionID: cmd.ExternalSessionID,
		WorkspacePath:     cmd.WorkspacePath,
	})
	if err != nil {
		n.log.Warn("history replay failed", "session_id", cmd.SessionID, "err", err)
		resp.Error = err.Error()
		send()
		return
	}
	resp.Messages = msgs
	n.log.Info("history replayed", "session_id", cmd.SessionID, "messages", len(msgs))
	send()
}
