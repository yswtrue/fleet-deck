package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"fleetdeck/internal/protocol"
)

// This file is the session history proxy. Session content is deliberately
// never persisted server-side (privacy model: the hub stores task metadata and
// status-class events only), so GET /api/sessions/{id}/messages serves either
// a replay of the agent's own session store — fetched on demand through the
// owning node — or, for live sessions and replay failures, the hub's ephemeral
// text buffer (textbuffer.go).

// historyRequestTimeout bounds one proxied replay round trip (the node may
// spawn a short-lived agent process to replay).
const historyRequestTimeout = 60 * time.Second

// handleListMessages serves a session's conversation, oldest first.
//
// Finished sessions replay from the agent's session store (the only durable
// record); live sessions read the ephemeral buffer, which holds every complete
// message that transited the hub since the session went live. A finished
// session with neither yields 503 — history living only on the node is
// unavailable when the node cannot serve it, by design.
func (h *Hub) handleListMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	limit := intQuery(r, "limit", 100)

	session, err := h.sessionVisibleTo(r.Context(), userOf(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	finished := session.Status == protocol.SessionDone || session.Status == protocol.SessionFailed

	if finished {
		ctx, cancel := context.WithTimeout(r.Context(), historyRequestTimeout)
		defer cancel()
		msgs, err := h.replaySessionHistory(ctx, session)
		if err == nil {
			writeJSON(w, http.StatusOK, limitMessages(msgs, limit))
			return
		}
		h.log.Warn("history replay unavailable; falling back to text buffer",
			"session_id", sessionID, "err", err)
	}

	buf := h.textBuffer(r.Context(), sessionID)
	if finished && len(buf.Messages) == 0 {
		writeError(w, http.StatusServiceUnavailable, "session history unavailable")
		return
	}
	writeJSON(w, http.StatusOK, limitMessages(buf.Messages, limit))
}

// limitMessages applies the endpoint's limit semantics: up to limit messages,
// oldest first; a limit <= 0 yields none.
func limitMessages(msgs []protocol.Message, limit int) []protocol.Message {
	// A live session's buffer starts empty; marshal it as [] rather than null so
	// clients can treat the response as an array unconditionally.
	if msgs == nil {
		msgs = []protocol.Message{}
	}
	if limit <= 0 {
		return []protocol.Message{}
	}
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return msgs
}

// replaySessionHistory fetches a session's conversation from the owning node,
// which replays the agent-side session on demand.
func (h *Hub) replaySessionHistory(ctx context.Context, session protocol.Session) ([]protocol.Message, error) {
	if session.ExternalSessionID == "" {
		return nil, errors.New("session has no agent-side session id")
	}
	workspacePath := ""
	if task, err := h.store.Queries().GetTask(ctx, session.TaskID); err == nil {
		if ws, err := h.store.Queries().GetWorkspace(ctx, task.WorkspaceID); err == nil {
			workspacePath = ws.Path
		}
	}
	req := protocol.SessionHistoryRequest{
		ReqID:             fmt.Sprintf("hist-%d", time.Now().UnixNano()),
		SessionID:         session.ID,
		AgentType:         session.AgentType,
		ExternalSessionID: session.ExternalSessionID,
		WorkspacePath:     workspacePath,
	}
	resp, err := h.requestSessionHistory(ctx, session.NodeID, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Messages, nil
}

// requestSessionHistory sends a history request to a node and waits for the
// response carrying the same ReqID. An offline node or an expired context are
// errors.
func (h *Hub) requestSessionHistory(ctx context.Context, nodeID string, req protocol.SessionHistoryRequest) (protocol.SessionHistoryResponse, error) {
	ch := make(chan protocol.SessionHistoryResponse, 1)
	h.histMu.Lock()
	h.histWait[req.ReqID] = ch
	h.histMu.Unlock()
	defer func() {
		h.histMu.Lock()
		delete(h.histWait, req.ReqID)
		h.histMu.Unlock()
	}()

	if !h.sendToNode(nodeID, protocol.NewEnvelope(protocol.MsgSessionHistory, req)) {
		return protocol.SessionHistoryResponse{}, fmt.Errorf("node %s is offline", nodeID)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return protocol.SessionHistoryResponse{}, fmt.Errorf("history request timed out: %w", ctx.Err())
	}
}

// handleHistoryResponse routes a node's history response to its waiter.
// A response nobody waits for (already timed out) is dropped.
func (h *Hub) handleHistoryResponse(resp protocol.SessionHistoryResponse) {
	h.histMu.Lock()
	ch := h.histWait[resp.ReqID]
	h.histMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}
