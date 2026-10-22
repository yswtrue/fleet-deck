package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Single-user dev tool: accept any origin.
	CheckOrigin: func(*http.Request) bool { return true },
}

const (
	wsPingInterval = 30 * time.Second
	wsWriteTimeout = 10 * time.Second
	wsReadTimeout  = 90 * time.Second
)

// wireEvent is the hub-side decode of a protocol.Event arriving from a node.
// Data stays raw so it can be persisted and re-broadcast without losing shape.
type wireEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	Ts   time.Time       `json:"ts"`
}

// handleWebWS serves /ws/web. Browsers cannot set WebSocket headers, so the
// bearer token arrives as ?token=; the upgrade is rejected with 401 when it
// is missing, unknown, expired, or revoked. On connect the client receives a
// hello carrying the node list visible to that user (all nodes for admins,
// the bound set for anyone else); thereafter every hub broadcast the user may
// see is forwarded as a protocol.Event JSON message.
func (h *Hub) handleWebWS(w http.ResponseWriter, r *http.Request) {
	user, _, _ := h.userFromToken(r.Context(), r.URL.Query().Get("token"))
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	nodes, err := h.nodesVisibleTo(r.Context(), user)
	if err != nil {
		h.log.Warn("hello: list nodes", "err", err)
		nodes = []protocol.Node{}
	}
	hello := map[string]any{
		"type": "hello",
		"data": map[string]any{"nodes": nodes},
	}
	_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := c.WriteJSON(hello); err != nil {
		return
	}

	events, unsubscribe := h.bus.Subscribe(h.webEventScope(user))
	defer unsubscribe()

	// Drain inbound frames (and pongs) so the read side notices client close.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-closed:
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.WriteJSON(evt); err != nil {
				return
			}
		case <-ping.C:
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleNodeWS serves /ws/node: registration handshake, then bidirectional
// command/event traffic. The hub is the only DB writer; inbound node messages
// are persisted and broadcast here.
func (h *Hub) handleNodeWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	// First frame must be a register_request.
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	var regEnv protocol.Envelope
	if err := c.ReadJSON(&regEnv); err != nil {
		return
	}
	if regEnv.Type != protocol.MsgRegisterRequest {
		return
	}
	var req protocol.RegisterRequest
	if err := regEnv.Decode(&req); err != nil {
		return
	}

	registered, issued, err := h.registerNode(ctx, req)
	if err != nil {
		h.log.Warn("node registration rejected", "name", req.Name, "err", err)
		// Tell the node why so its log shows an actionable cause instead of a
		// bare disconnect; the socket closes right after.
		respEnv := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{
			Error: err.Error(),
		})
		_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		_ = c.WriteJSON(respEnv)
		return
	}

	respEnv := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{
		NodeID: registered.ID,
		Token:  issued,
	})
	_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := c.WriteJSON(respEnv); err != nil {
		return
	}
	_ = c.SetWriteDeadline(time.Time{})

	nc := h.addNode(registered.ID, c)
	h.log.Info("node online", "node_id", registered.ID, "name", registered.Name)
	h.bus.Publish(protocol.Event{
		Type: protocol.EvNodeStatusChanged,
		Data: protocol.NodeStatusChanged{NodeID: registered.ID, Status: protocol.NodeOnline},
	})

	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	_ = c.SetReadDeadline(time.Now().Add(wsReadTimeout))

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.nodeWriteLoop(c, nc)
	}()

	for {
		var env protocol.Envelope
		if err := c.ReadJSON(&env); err != nil {
			break
		}
		h.handleNodeMessage(ctx, registered.ID, env)
	}

	// Unblock this connection's writer, then reflect the disconnect — but only
	// when this handler still owns the node: a reconnect supersedes it, and a
	// stale handler must neither evict the live connection nor mark the node
	// offline.
	current := h.removeNode(nc)
	<-writerDone
	if !current {
		return
	}
	h.log.Info("node offline", "node_id", registered.ID, "name", registered.Name)
	// Fail its sessions only if the node does not come back within the grace
	// period: a reconnect keeps the adapters running on the node.
	h.scheduleOffline(registered.ID)
}

// nodeWriteLoop drains a node's outbound queue and sends periodic pings. It
// exits when the connection is torn down (done closed) or a write fails.
func (h *Hub) nodeWriteLoop(c *websocket.Conn, nc *nodeConn) {
	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-nc.done:
			return
		case raw := <-nc.send:
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		case <-ping.C:
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Hub) handleNodeMessage(ctx context.Context, nodeID string, env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgEvent:
		var we wireEvent
		if err := env.Decode(&we); err != nil {
			h.log.Warn("bad node event", "node_id", nodeID, "err", err)
			return
		}
		h.applyEvent(ctx, we)
	case protocol.MsgTodoUpdate:
		var tu protocol.TodoUpdate
		if err := env.Decode(&tu); err != nil {
			h.log.Warn("bad todo_update", "node_id", nodeID, "err", err)
			return
		}
		h.applyTodoUpdate(ctx, tu)
	case protocol.MsgSessionStarted:
		var st protocol.SessionStarted
		if err := env.Decode(&st); err != nil {
			h.log.Warn("bad session.started", "node_id", nodeID, "err", err)
			return
		}
		if st.ExternalSessionID == "" {
			return
		}
		// Persisted so a finished session can be replayed through the node
		// later; without it GET /api/sessions/{id}/messages has no way back to
		// the agent-side session and answers 503.
		if err := h.store.Queries().UpdateSessionExternalID(ctx, st.SessionID, st.ExternalSessionID); err != nil {
			h.log.Warn("record external session id", "session_id", st.SessionID, "err", err)
		}
	case protocol.MsgSessionEvent:
		var sm protocol.SessionMessage
		if err := env.Decode(&sm); err != nil {
			h.log.Warn("bad session.event", "node_id", nodeID, "err", err)
			return
		}
		payload, err := json.Marshal(sm)
		if err != nil {
			h.log.Warn("encode session.event", "node_id", nodeID, "err", err)
			return
		}
		h.applyEvent(ctx, wireEvent{Type: protocol.EvSessionMessage, Data: payload, Ts: time.Now().UTC()})
	case protocol.MsgSessionHistoryResp:
		var resp protocol.SessionHistoryResponse
		if err := env.Decode(&resp); err != nil {
			h.log.Warn("bad session.history_resp", "node_id", nodeID, "err", err)
			return
		}
		h.handleHistoryResponse(resp)
	case protocol.MsgFSListResp:
		var resp protocol.FSListResponse
		if err := env.Decode(&resp); err != nil {
			h.log.Warn("bad fs.list_resp", "node_id", nodeID, "err", err)
			return
		}
		h.handleFSListResponse(resp)
	default:
		h.log.Debug("ignoring unknown node message", "node_id", nodeID, "type", env.Type)
	}
}

// applyEvent applies one node-reported session event and echoes it to web
// subscribers. Storage policy (privacy model): session content is never
// persisted to Postgres — complete messages go to the ephemeral text buffer
// only, text deltas are not stored at all — while status-class events are
// persisted to the per-session event stream with text payloads stripped.
// The agent's own session store on the node is the durable record of the
// conversation, replayed on demand (see history.go).
func (h *Hub) applyEvent(ctx context.Context, we wireEvent) {
	q := h.store.Queries()
	if we.Ts.IsZero() {
		we.Ts = time.Now().UTC()
	}
	sessionID := sessionIDOf(we)
	if sessionID == "" {
		// Every node-reported session event must carry a session id; a drop
		// here is a payload-shape bug and must not stay silent.
		h.log.Warn("dropping node event without session id", "type", we.Type)
	}

	if sessionID != "" {
		switch we.Type {
		case protocol.EvSessionMessage:
			var m protocol.SessionMessage
			if json.Unmarshal(we.Data, &m) == nil {
				m.SessionID = sessionID
				m.Message = normalizeMessage(sessionID, m.Message, we.Ts)
				h.appendBufferedMessage(ctx, sessionID, m.Message)
				// Re-broadcast the normalized message so live clients and the
				// text buffer agree on ids.
				if payload, err := json.Marshal(m); err == nil {
					we.Data = payload
				}
			}
		case protocol.EvSessionDelta:
			var d protocol.SessionDelta
			if json.Unmarshal(we.Data, &d) == nil {
				h.appendPartialText(sessionID, d.Text)
			}
		case protocol.EvSessionStateChanged:
			var s protocol.SessionStateChanged
			if json.Unmarshal(we.Data, &s) == nil {
				h.applySessionStatus(ctx, sessionID, s.Status)
				if s.Status != protocol.SessionAwaitingUser {
					h.setPendingInput(ctx, sessionID, "")
				}
				// A terminal turn ends the stream: drop the partial so a reload
				// does not resurrect text the live view has already cleared.
				if s.Status == protocol.SessionDone || s.Status == protocol.SessionFailed {
					h.clearPartialText(ctx, sessionID)
				}
			}
		case protocol.EvSessionUserInputRequest:
			var u protocol.SessionUserInputRequested
			// Derive the awaiting_user status before exposing the prompt:
			// handleGetSession reads the text buffer before the session row, so
			// a snapshot that carries the ask always carries the matching
			// status (no prompt on a still-running session).
			h.applySessionStatus(ctx, sessionID, protocol.SessionAwaitingUser)
			if json.Unmarshal(we.Data, &u) == nil {
				h.setPendingInput(ctx, sessionID, u.Prompt)
			}
		case protocol.EvSessionUsage:
			var u protocol.SessionUsage
			if json.Unmarshal(we.Data, &u) == nil {
				h.applyUsage(ctx, sessionID, u)
			}
		}
		if persistEvent(we.Type) {
			payload := we.Data
			if we.Type == protocol.EvSessionUserInputRequest {
				// The ask's prompt is content: persist only its occurrence
				// (the empty prompt keeps the web's event contract intact; the
				// actual ask lives in the ephemeral buffer and travels live).
				payload = strippedUserInputPayload(sessionID)
			}
			if seq, err := q.MaxEventSeq(ctx, sessionID); err == nil {
				if _, err := q.AppendEvent(ctx, sessionID, we.Type, payload, seq+1); err != nil {
					h.log.Warn("persist event", "session_id", sessionID, "type", we.Type, "err", err)
				}
			}
		}
	}

	h.bus.Publish(protocol.Event{Type: we.Type, Data: json.RawMessage(we.Data), Ts: time.Now().UTC()})
}

// persistEvent reports whether an event type belongs to the server-side event
// stream (the status timeline used for audit and gap-fill). Text-carrying
// events — session.delta, session.message — are deliberately excluded.
func persistEvent(eventType string) bool {
	switch eventType {
	case protocol.EvSessionStateChanged, protocol.EvSessionUserInputRequest, protocol.EvSessionUsage:
		return true
	}
	return false
}

// strippedUserInputPayload is the persisted form of a user_input_requested
// event: the ask's occurrence without its prompt (content).
func strippedUserInputPayload(sessionID string) json.RawMessage {
	payload, _ := json.Marshal(protocol.SessionUserInputRequested{
		SessionID: sessionID,
		Prompt:    "",
	})
	return payload
}

// applyUsage persists one reported turn's token usage as a token_usage row.
// The session must resolve (its task_id backs the row); reports for unknown
// sessions are dropped with a warning. An empty model falls back to the
// session's agent_type — the documented fallback (numbers are never touched).
func (h *Hub) applyUsage(ctx context.Context, sessionID string, u protocol.SessionUsage) {
	q := h.store.Queries()
	session, err := q.GetSession(ctx, sessionID)
	if err != nil {
		h.log.Warn("usage for unknown session", "session_id", sessionID, "err", err)
		return
	}
	model := u.Model
	if model == "" {
		model = session.AgentType
	}
	if _, err := q.InsertTokenUsage(ctx, store.InsertTokenUsageParams{
		SessionID:         sessionID,
		TaskID:            session.TaskID,
		Model:             model,
		PromptTokens:      u.PromptTokens,
		CompletionTokens:  u.CompletionTokens,
		TotalTokens:       u.TotalTokens,
		CachedReadTokens:  u.CachedReadTokens,
		CachedWriteTokens: u.CachedWriteTokens,
	}); err != nil {
		h.log.Warn("persist token usage", "session_id", sessionID, "err", err)
	}
}

// moveTaskTo updates a task's status and broadcasts task.status_changed when the
// task actually moves. It is the shared write+broadcast used by the
// session-driven mapping and the user-reply resume.
func (h *Hub) moveTaskTo(ctx context.Context, task protocol.Task, to protocol.TaskStatus) {
	if task.Status == to {
		return
	}
	updated, err := h.store.Queries().UpdateTaskStatus(ctx, task.ID, to)
	if err != nil {
		h.log.Warn("update task status", "task_id", task.ID, "err", err)
		return
	}
	h.invalidateTasks(ctx, task.WorkspaceID)
	h.bus.Publish(protocol.Event{
		Type: protocol.EvTaskStatusChanged,
		Data: protocol.TaskStatusChanged{TaskID: updated.ID, From: task.Status, To: updated.Status},
	})
}

// syncTaskFromSession maps a session lifecycle status onto its owning task and
// broadcasts task.status_changed when the task moves. This is the single place
// the session -> task status rule is applied.
//
// Completion gate: a session that ends (done) does not finish a task whose
// mirrored subtasks are still open — the task parks in awaiting_user instead
// so the user can see the unfinished work and continue the session (which the
// /send resume path reopens). A failed session marks its task failed: the
// operator resolves it (done) or continues the conversation.
func (h *Hub) syncTaskFromSession(ctx context.Context, sessionID string, status protocol.SessionStatus) {
	task, err := h.store.Queries().GetTaskBySessionID(ctx, sessionID)
	if err != nil {
		return
	}
	to := taskStatusFor(status)
	if status == protocol.SessionDone && to == protocol.TaskDone && h.taskHasOpenSubtasks(ctx, task.ID) {
		to = protocol.TaskAwaitingUser
	}
	h.moveTaskTo(ctx, task, to)
}

// taskHasOpenSubtasks reports whether the task has at least one mirrored
// subtask that is not done. Subtask rows disappear when their agent-side todo
// is removed, so only present rows count.
func (h *Hub) taskHasOpenSubtasks(ctx context.Context, taskID string) bool {
	subs, err := h.store.Queries().ListSubtasksByTask(ctx, taskID)
	if err != nil {
		// On a read failure stay with the ungated mapping: a stuck task is
		// worse than an early done.
		h.log.Warn("completion gate: list subtasks", "task_id", taskID, "err", err)
		return false
	}
	for _, sub := range subs {
		if sub.Status != protocol.SubtaskDone {
			return true
		}
	}
	return false
}

// reopenTaskOnResume flips a finished task back to running when its session is
// revived through /send. It goes through moveTaskTo (the internal
// session->task derivation), deliberately bypassing the manual PATCH table
// where done is terminal.
func (h *Hub) reopenTaskOnResume(ctx context.Context, sessionID string) {
	task, err := h.store.Queries().GetTaskBySessionID(ctx, sessionID)
	if err != nil {
		return
	}
	if task.Status != protocol.TaskDone && task.Status != protocol.TaskFailed {
		return
	}
	h.moveTaskTo(ctx, task, protocol.TaskRunning)
}

// resumeTaskOnReply bumps the parent task awaiting_user -> running in lock-step
// with a user reply (locked Gate 2 decision: the board must clear "waiting for
// user" immediately instead of waiting for the next node status report). It is
// idempotent: a task already running or done is left alone, and a missing task
// row is ignored so the reply itself still succeeds.
func (h *Hub) resumeTaskOnReply(ctx context.Context, sessionID string) {
	task, err := h.store.Queries().GetTaskBySessionID(ctx, sessionID)
	if err != nil {
		return
	}
	if task.Status != protocol.TaskAwaitingUser {
		return
	}
	h.moveTaskTo(ctx, task, taskStatusFor(protocol.SessionRunning))
}

// applySessionStatus updates the session row and derives the task's 4-state
// status from it, broadcasting task.status_changed when it moves.
func (h *Hub) applySessionStatus(ctx context.Context, sessionID string, status protocol.SessionStatus) {
	q := h.store.Queries()
	var endedAt *time.Time
	if status == protocol.SessionDone || status == protocol.SessionFailed {
		t := time.Now().UTC()
		endedAt = &t
	}
	if err := q.UpdateSessionStatus(ctx, sessionID, status, endedAt); err != nil {
		h.log.Warn("update session status", "session_id", sessionID, "err", err)
	}
	h.syncTaskFromSession(ctx, sessionID, status)
}

func taskStatusFor(s protocol.SessionStatus) protocol.TaskStatus {
	switch s {
	case protocol.SessionWaiting:
		return protocol.TaskWaiting
	case protocol.SessionAwaitingUser:
		return protocol.TaskAwaitingUser
	case protocol.SessionDone:
		return protocol.TaskDone
	case protocol.SessionFailed:
		return protocol.TaskFailed
	default:
		return protocol.TaskRunning
	}
}

// applyTodoUpdate mirrors a full todo-list snapshot onto the task's subtasks by
// diffing on external_id: upsert present todos, delete the rest.
func (h *Hub) applyTodoUpdate(ctx context.Context, tu protocol.TodoUpdate) {
	q := h.store.Queries()
	task, err := q.GetTaskBySessionID(ctx, tu.SessionID)
	if err != nil {
		h.log.Warn("todo update for unknown session", "session_id", tu.SessionID, "err", err)
		return
	}

	current, err := q.ListSubtasksByTask(ctx, task.ID)
	if err != nil {
		h.log.Warn("list subtasks", "task_id", task.ID, "err", err)
		return
	}
	byExternal := make(map[string]protocol.Subtask, len(current))
	for _, s := range current {
		if s.ExternalID != "" {
			byExternal[s.ExternalID] = s
		}
	}

	seen := make(map[string]bool, len(tu.Todos))
	for i, todo := range tu.Todos {
		if todo.ID == "" {
			continue
		}
		seen[todo.ID] = true
		sub, err := q.UpsertSubtaskByExternalID(ctx, store.UpsertSubtaskByExternalIDParams{
			TaskID:     task.ID,
			Title:      todo.Title,
			Status:     todo.Status,
			ExternalID: todo.ID,
			SortOrder:  i,
		})
		if err != nil {
			h.log.Warn("upsert subtask", "task_id", task.ID, "external_id", todo.ID, "err", err)
			continue
		}
		h.bus.Publish(protocol.Event{
			Type: protocol.EvSubtaskUpdated,
			Data: protocol.SubtaskUpdated{TaskID: task.ID, Subtask: sub},
		})
	}

	for external, sub := range byExternal {
		if seen[external] {
			continue
		}
		if err := q.DeleteSubtaskByExternalID(ctx, task.ID, external); err != nil {
			h.log.Warn("delete subtask", "task_id", task.ID, "external_id", external, "err", err)
			continue
		}
		h.bus.Publish(protocol.Event{
			Type: protocol.EvSubtaskUpdated,
			Data: protocol.SubtaskUpdated{TaskID: task.ID, Subtask: sub, Removed: true},
		})
	}

	// Persist the snapshot so /api/sessions/{id}/events can gap-fill todo state.
	if payload, err := json.Marshal(tu); err == nil {
		if seq, err := q.MaxEventSeq(ctx, tu.SessionID); err == nil {
			if _, err := q.AppendEvent(ctx, tu.SessionID, "todo.update", payload, seq+1); err != nil {
				h.log.Warn("persist todo.update", "session_id", tu.SessionID, "err", err)
			}
		}
	}
}

// sessionIDOf extracts the session id from a hub↔web event payload, or "".
func sessionIDOf(we wireEvent) string {
	switch we.Type {
	case protocol.EvSessionDelta:
		var d protocol.SessionDelta
		_ = json.Unmarshal(we.Data, &d)
		return d.SessionID
	case protocol.EvSessionThought:
		var th protocol.SessionThought
		_ = json.Unmarshal(we.Data, &th)
		return th.SessionID
	case protocol.EvSessionMessage:
		var m protocol.SessionMessage
		_ = json.Unmarshal(we.Data, &m)
		return m.SessionID
	case protocol.EvSessionStateChanged:
		var s protocol.SessionStateChanged
		_ = json.Unmarshal(we.Data, &s)
		return s.SessionID
	case protocol.EvSessionUserInputRequest:
		var u protocol.SessionUserInputRequested
		_ = json.Unmarshal(we.Data, &u)
		return u.SessionID
	case protocol.EvSessionUsage:
		var u protocol.SessionUsage
		_ = json.Unmarshal(we.Data, &u)
		return u.SessionID
	default:
		return ""
	}
}

// ---- broadcast scoping -----------------------------------------------------

// eventNodeID maps a hub event onto the node whose subtree owns it — the
// scoping key for /ws/web fan-out. Node events carry the node id directly;
// task events resolve through the task's workspace; session events through
// the session row (falling back to its task). Anything unresolvable (unknown
// payload shape, vanished rows) returns "": such events are delivered to
// admins only and dropped for non-admin subscribers.
func (h *Hub) eventNodeID(evt protocol.Event) string {
	// Publish has no request context; resolve under a bounded detached one so
	// a slow query cannot wedge a caller's cancellation into the fan-out.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch evt.Type {
	case protocol.EvNodeStatusChanged:
		var d protocol.NodeStatusChanged
		if decodeEventPayload(evt.Data, &d) {
			return d.NodeID
		}
	case protocol.EvTaskCreated:
		var d protocol.TaskCreated
		if decodeEventPayload(evt.Data, &d) {
			if d.Task.WorkspaceID != "" {
				return h.workspaceNodeID(ctx, d.Task.WorkspaceID)
			}
			return h.taskNodeID(ctx, d.Task.ID)
		}
	case protocol.EvTaskStatusChanged:
		var d protocol.TaskStatusChanged
		if decodeEventPayload(evt.Data, &d) {
			return h.taskNodeID(ctx, d.TaskID)
		}
	case protocol.EvSubtaskUpdated:
		var d protocol.SubtaskUpdated
		if decodeEventPayload(evt.Data, &d) {
			return h.taskNodeID(ctx, d.TaskID)
		}
	case protocol.EvSessionDelta, protocol.EvSessionMessage,
		protocol.EvSessionStateChanged, protocol.EvSessionUserInputRequest,
		protocol.EvSessionUsage:
		var d struct {
			SessionID string `json:"session_id"`
		}
		if decodeEventPayload(evt.Data, &d) && d.SessionID != "" {
			if session, err := h.store.Queries().GetSession(ctx, d.SessionID); err == nil {
				return session.NodeID
			}
			if task, err := h.store.Queries().GetTaskBySessionID(ctx, d.SessionID); err == nil {
				return h.workspaceNodeID(ctx, task.WorkspaceID)
			}
		}
	}
	return ""
}

// workspaceNodeID resolves a workspace to its node id ("" when unknown).
func (h *Hub) workspaceNodeID(ctx context.Context, workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	workspace, err := h.store.Queries().GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return workspace.NodeID
}

// taskNodeID resolves a task to the node id of its workspace ("" when unknown).
func (h *Hub) taskNodeID(ctx context.Context, taskID string) string {
	if taskID == "" {
		return ""
	}
	task, err := h.store.Queries().GetTask(ctx, taskID)
	if err != nil {
		return ""
	}
	return h.workspaceNodeID(ctx, task.WorkspaceID)
}

// decodeEventPayload unmarshals an event payload into out. Event.Data is a
// typed struct at hub call sites and raw JSON when re-broadcast from a node;
// both encode the same wire shape.
func decodeEventPayload(data any, out any) bool {
	raw, ok := data.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(data); err != nil {
			return false
		}
	}
	return json.Unmarshal(raw, out) == nil
}
