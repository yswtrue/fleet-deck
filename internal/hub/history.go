package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
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

// transcriptRequestTimeout bounds one live-transcript round trip. The node
// serves it from memory, so a slow/mute node is a fallback case, never a
// reason to hold the request open.
const transcriptRequestTimeout = 2 * time.Second

// transcriptReqSeq mints unique correlation ids for concurrent transcript
// reads; two GET /messages calls must not overwrite each other's waiter.
var transcriptReqSeq atomic.Int64

// handleListMessages serves a session's conversation, oldest first.
//
// A running session's authoritative transcript lives in the owning node's
// in-memory cache: the node sees every adapter event in order and survives a
// hub restart, so a browser that opens mid-run renders exactly what the agent
// produced (including tool calls) instead of a hub-side reconstruction. The
// hub's ephemeral buffer is merged in as a fallback for rows the node never
// received (a reply queued while the node was offline).
//
// A finished session still replays from the agent's own session store — the
// durable record — with the node cache (tool calls, output the agent does not
// record) and the hub buffer merged around it. With no source at all the
// endpoint answers 503 rather than inventing a transcript.
func (h *Hub) handleListMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	// A limited read is a tail window (the newest messages). No limit means
	// the whole transcript: the session view renders every row, so silently
	// capping it at a page hid the final turns of a long task.
	limit := intQuery(r, "limit", 0)

	session, err := h.sessionVisibleTo(r.Context(), userOf(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	finished := session.Status == protocol.SessionDone || session.Status == protocol.SessionFailed

	// The node cache is cheap (memory) and is the only source that keeps live
	// tool-call rows; request it for finished sessions too, then let the agent
	// replay dominate the merged chronology.
	live, liveOK := h.nodeTranscript(r.Context(), session)
	buf := h.textBuffer(r.Context(), sessionID)

	if finished {
		ctx, cancel := context.WithTimeout(r.Context(), historyRequestTimeout)
		defer cancel()
		msgs, err := h.replaySessionHistory(ctx, session)
		if err == nil {
			// The agent's own replay is the model conversation; the node cache
			// and hub buffer hold everything that actually transited the wire
			// (including output an agent does not record, e.g. pi-acp's startup
			// banner, and tool calls ACP does not replay).
			extras := mergeLiveTranscripts(live.Messages, buf.Messages)
			writeJSON(w, http.StatusOK, limitMessages(mergeHistory(msgs, extras), limit))
			return
		}
		h.log.Warn("history replay unavailable; falling back to live transcript",
			"session_id", sessionID, "err", err)
	}

	if liveOK {
		writeJSON(w, http.StatusOK, limitMessages(mergeLiveTranscripts(live.Messages, buf.Messages), limit))
		return
	}
	if finished && len(buf.Messages) == 0 {
		writeError(w, http.StatusServiceUnavailable, "session history unavailable")
		return
	}
	writeJSON(w, http.StatusOK, limitMessages(buf.Messages, limit))
}

// nodeTranscript asks the owning node for its live transcript. An offline
// node, a timeout, or a request the node rejects reports ok=false so the
// caller falls back to the hub buffer. An empty transcript is a success.
func (h *Hub) nodeTranscript(ctx context.Context, session protocol.Session) (protocol.SessionTranscriptResponse, bool) {
	if !h.nodeOnline(session.NodeID) {
		return protocol.SessionTranscriptResponse{}, false
	}
	reqCtx, cancel := context.WithTimeout(ctx, transcriptRequestTimeout)
	defer cancel()
	resp, err := h.requestSessionTranscript(reqCtx, session.NodeID, protocol.SessionTranscriptRequest{
		ReqID:     fmt.Sprintf("transcript-%d", transcriptReqSeq.Add(1)),
		SessionID: session.ID,
	})
	if err != nil {
		h.log.Debug("live transcript unavailable; using hub buffer",
			"session_id", session.ID, "node_id", session.NodeID, "err", err)
		return protocol.SessionTranscriptResponse{}, false
	}
	if resp.Error != "" {
		h.log.Debug("node rejected live transcript request",
			"session_id", session.ID, "err", resp.Error)
		return protocol.SessionTranscriptResponse{}, false
	}
	return resp, true
}

// mergeLiveTranscripts merges the node's authoritative transcript with the
// hub's fallback buffer. Node rows win (same id), and a buffered row the node
// never saw is inserted at its timestamp instead of being appended after the
// node's newer rows. This keeps a reply queued while the node was offline in
// chronological position once the node comes back.
func mergeLiveTranscripts(node, buffered []protocol.Message) []protocol.Message {
	out := make([]protocol.Message, 0, len(node)+len(buffered))
	seenID := make(map[string]bool, len(node))
	seenKey := make(map[string]bool, len(node))
	for _, m := range node {
		out = append(out, m)
		if m.ID != "" {
			seenID[m.ID] = true
		}
		seenKey[messageKey(m)] = true
	}
	for _, m := range buffered {
		if m.ID != "" && seenID[m.ID] {
			continue
		}
		key := messageKey(m)
		if m.ID == "" && seenKey[key] {
			continue
		}
		if m.ID != "" {
			seenID[m.ID] = true
		}
		seenKey[key] = true
		out = insertByTime(out, m)
	}
	return out
}

// insertByTime inserts m before the first row with a later non-zero timestamp.
// Rows without timestamps (replays) stay in their existing order.
func insertByTime(list []protocol.Message, m protocol.Message) []protocol.Message {
	if m.CreatedAt.IsZero() {
		return append(list, m)
	}
	for i, cur := range list {
		if cur.CreatedAt.IsZero() || !cur.CreatedAt.After(m.CreatedAt) {
			continue
		}
		list = append(list, protocol.Message{})
		copy(list[i+1:], list[i:])
		list[i] = m
		return list
	}
	return append(list, m)
}

// mergeHistory combines the agent's replay with the hub's live buffer without
// losing either source's chronology. The two lists are aligned on role+content
// (the sources mint different ids); buffered matches yield to the replay row,
// and a buffered row the replay lacks is emitted at its buffer position
// relative to the surrounding matches. Appending those rows at the end — the
// previous behavior — put a startup banner or a live-only message after the
// answer it preceded, and put an earlier agent-side session after a newer one.
func mergeHistory(replay, buffered []protocol.Message) []protocol.Message {
	// anchor[i] is buffered[i]'s replay index, or -1 when the replay lacks it.
	// Matching is monotonic: a repeated message pairs with the next replay
	// occurrence, so identical turns keep their order.
	anchor := make([]int, len(buffered))
	next := 0
	for i, m := range buffered {
		anchor[i] = -1
		key := messageKey(m)
		for j := next; j < len(replay); j++ {
			if messageKey(replay[j]) == key {
				anchor[i] = j
				next = j + 1
				break
			}
		}
	}

	out := make([]protocol.Message, 0, len(replay)+len(buffered))
	ri := 0
	for i, m := range buffered {
		if anchor[i] < 0 {
			out = append(out, m)
			continue
		}
		out = append(out, replay[ri:anchor[i]]...)
		out = append(out, replay[anchor[i]])
		ri = anchor[i] + 1
	}
	return append(out, replay[ri:]...)
}

// messageKey identifies a message across the two history sources, which do not
// share ids. Tool rows use their call id: their content may be empty or
// change with the call's title, and two different calls would otherwise
// collide.
func messageKey(m protocol.Message) string {
	if m.Tool != nil {
		return string(m.Role) + "\x00tool:" + m.Tool.ID
	}
	return string(m.Role) + "\x00" + m.Content
}

// limitMessages applies the endpoint's limit semantics: a positive limit is a
// window over the newest messages (still oldest first), because a caller
// reading a session wants its latest turns — `fleetdeck_message_tail` says so
// explicitly, and the web reloads a transcript to continue where it left off.
// A non-positive limit yields the whole transcript.
func limitMessages(msgs []protocol.Message, limit int) []protocol.Message {
	// A live session's buffer starts empty; marshal it as [] rather than null so
	// clients can treat the response as an array unconditionally.
	if msgs == nil {
		msgs = []protocol.Message{}
	}
	if limit <= 0 || len(msgs) <= limit {
		return msgs
	}
	return msgs[len(msgs)-limit:]
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

// requestSessionTranscript sends a live-transcript request to a node and waits
// for the response carrying the same ReqID. An offline node or an expired
// context are errors; the caller falls back to the hub buffer.
func (h *Hub) requestSessionTranscript(ctx context.Context, nodeID string, req protocol.SessionTranscriptRequest) (protocol.SessionTranscriptResponse, error) {
	ch := make(chan protocol.SessionTranscriptResponse, 1)
	h.transcriptMu.Lock()
	h.transcriptWait[req.ReqID] = ch
	h.transcriptMu.Unlock()
	defer func() {
		h.transcriptMu.Lock()
		delete(h.transcriptWait, req.ReqID)
		h.transcriptMu.Unlock()
	}()

	if !h.sendToNode(nodeID, protocol.NewEnvelope(protocol.MsgSessionTranscript, req)) {
		return protocol.SessionTranscriptResponse{}, fmt.Errorf("node %s is offline", nodeID)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return protocol.SessionTranscriptResponse{}, fmt.Errorf("transcript request timed out: %w", ctx.Err())
	}
}

// handleTranscriptResponse routes a node's transcript response to its waiter.
// A response nobody waits for (already timed out) is dropped.
func (h *Hub) handleTranscriptResponse(resp protocol.SessionTranscriptResponse) {
	h.transcriptMu.Lock()
	ch := h.transcriptWait[resp.ReqID]
	h.transcriptMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}
