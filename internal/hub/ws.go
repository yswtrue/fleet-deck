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
// hello carrying the current node list; thereafter every hub broadcast is
// forwarded as a protocol.Event JSON message.
func (h *Hub) handleWebWS(w http.ResponseWriter, r *http.Request) {
	if user, _, _ := h.userFromToken(r.Context(), r.URL.Query().Get("token")); user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	nodes, err := h.store.Queries().ListNodes(r.Context())
	if err != nil {
		h.log.Warn("hello: list nodes", "err", err)
		nodes = nil
	}
	hello := map[string]any{
		"type": "hello",
		"data": map[string]any{"nodes": nodes},
	}
	_ = c.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := c.WriteJSON(hello); err != nil {
		return
	}

	events, unsubscribe := h.bus.Subscribe()
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

	nc := h.addNode(registered.ID)
	defer h.removeNode(registered.ID)
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

	// Unblock the writer, then reflect the disconnect.
	h.removeNode(registered.ID)
	<-writerDone
	h.markNodeOffline(ctx, registered.ID)
	h.log.Info("node offline", "node_id", registered.ID, "name", registered.Name)
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
	default:
		h.log.Debug("ignoring unknown node message", "node_id", nodeID, "type", env.Type)
	}
}

// applyEvent persists a node-reported event against its session and applies its
// domain effect (message row, session/task state transition) before echoing it
// to web subscribers.
func (h *Hub) applyEvent(ctx context.Context, we wireEvent) {
	q := h.store.Queries()
	sessionID := sessionIDOf(we)
	if sessionID == "" {
		// Every node-reported session event must carry a session id; a drop
		// here is a payload-shape bug and must not stay silent.
		h.log.Warn("dropping node event without session id", "type", we.Type)
	}

	if sessionID != "" {
		if seq, err := q.MaxEventSeq(ctx, sessionID); err == nil {
			if _, err := q.AppendEvent(ctx, sessionID, we.Type, we.Data, seq+1); err != nil {
				h.log.Warn("persist event", "session_id", sessionID, "type", we.Type, "err", err)
			}
		}
		switch we.Type {
		case protocol.EvSessionMessage:
			var m protocol.SessionMessage
			if json.Unmarshal(we.Data, &m) == nil {
				if _, err := q.CreateMessage(ctx, store.CreateMessageParams{
					SessionID: sessionID,
					Role:      m.Message.Role,
					Content:   m.Message.Content,
				}); err != nil {
					h.log.Warn("persist message", "session_id", sessionID, "err", err)
				}
			}
		case protocol.EvSessionStateChanged:
			var s protocol.SessionStateChanged
			if json.Unmarshal(we.Data, &s) == nil {
				h.applySessionStatus(ctx, sessionID, s.Status)
			}
		case protocol.EvSessionUserInputRequest:
			h.applySessionStatus(ctx, sessionID, protocol.SessionAwaitingUser)
		}
	}

	h.bus.Publish(protocol.Event{Type: we.Type, Data: json.RawMessage(we.Data), Ts: time.Now().UTC()})
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
	h.bus.Publish(protocol.Event{
		Type: protocol.EvTaskStatusChanged,
		Data: protocol.TaskStatusChanged{TaskID: updated.ID, From: task.Status, To: updated.Status},
	})
}

// syncTaskFromSession maps a session lifecycle status onto its owning task and
// broadcasts task.status_changed when the task moves. This is the single place
// the session -> task status rule is applied.
func (h *Hub) syncTaskFromSession(ctx context.Context, sessionID string, status protocol.SessionStatus) {
	task, err := h.store.Queries().GetTaskBySessionID(ctx, sessionID)
	if err != nil {
		return
	}
	h.moveTaskTo(ctx, task, taskStatusFor(status))
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
	case protocol.SessionDone, protocol.SessionFailed:
		return protocol.TaskDone
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
	default:
		return ""
	}
}
