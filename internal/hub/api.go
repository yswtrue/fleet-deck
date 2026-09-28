package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// routes registers every HTTP and WebSocket endpoint on mux. Auth gating is
// declared here, next to the route table, so the permission matrix is visible
// in one place:
//
//   - Open (no auth): GET /api/health, GET /api/auth/status,
//     POST /api/auth/setup, POST /api/auth/login, the SPA static files, and
//     /ws/node (node auth is the register-frame token, unchanged).
//   - requireAuth: every /api/* endpoint below plus /ws/web (?token=).
//   - requireAdmin: POST /api/nodes and the /api/users* management plane
//     (including node bindings). Non-admin access follows the user<->node
//     binding with subtree inheritance; see canReadNode / canReadWorkspace.
func (h *Hub) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", h.handleHealth)

	mux.HandleFunc("GET /api/auth/status", h.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/setup", h.handleAuthSetup)
	mux.HandleFunc("POST /api/auth/login", h.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", h.requireAuth(h.handleAuthLogout))
	mux.HandleFunc("GET /api/auth/me", h.requireAuth(h.handleAuthMe))

	mux.HandleFunc("GET /api/nodes", h.requireAuth(h.handleListNodes))
	mux.HandleFunc("GET /api/nodes/{id}", h.requireAuth(h.handleGetNode))
	mux.HandleFunc("POST /api/nodes", h.requireAdmin(h.handleCreateNode))

	mux.HandleFunc("GET /api/users", h.requireAdmin(h.handleListUsers))
	mux.HandleFunc("POST /api/users", h.requireAdmin(h.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", h.requireAdmin(h.handlePatchUser))
	mux.HandleFunc("DELETE /api/users/{id}", h.requireAdmin(h.handleDeleteUser))
	mux.HandleFunc("GET /api/users/{id}/nodes", h.requireAdmin(h.handleGetUserNodes))
	mux.HandleFunc("PUT /api/users/{id}/nodes", h.requireAdmin(h.handleSetUserNodes))

	mux.HandleFunc("GET /api/workspaces", h.requireAuth(h.handleListWorkspaces))
	mux.HandleFunc("GET /api/workspaces/{id}", h.requireWorkspaceAccess(h.handleGetWorkspace))
	mux.HandleFunc("POST /api/workspaces", h.requireAuth(h.handleCreateWorkspace))

	mux.HandleFunc("GET /api/tasks", h.requireAuth(h.handleListTasks))
	mux.HandleFunc("POST /api/tasks", h.requireAuth(h.handleCreateTask))
	mux.HandleFunc("PATCH /api/tasks/{id}", h.requireAuth(h.handlePatchTask))
	mux.HandleFunc("GET /api/tasks/{id}/subtasks", h.requireAuth(h.handleListSubtasksByTask))

	mux.HandleFunc("GET /api/sessions/{id}", h.requireAuth(h.handleGetSession))
	mux.HandleFunc("GET /api/sessions/{id}/messages", h.requireAuth(h.handleListMessages))
	mux.HandleFunc("GET /api/sessions/{id}/events", h.requireAuth(h.handleListEvents))
	mux.HandleFunc("POST /api/sessions/{id}/send", h.requireAuth(h.handleSendMessage))

	mux.HandleFunc("GET /ws/web", h.handleWebWS)
	mux.HandleFunc("GET /ws/node", h.handleNodeWS)

	// Unknown API paths (including the removed workspace-member endpoints)
	// answer 404 JSON instead of falling through to the SPA. Each method gets
	// its own fallback so the patterns stay more specific than the catch-all
	// "GET /" below.
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		mux.HandleFunc(method+" /api/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
	}

	// Embedded web UI; "/" is the catch-all, everything above is more specific.
	mux.HandleFunc("GET /", newSPAHandler(uiFS))
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *Hub) serverError(w http.ResponseWriter, msg string, err error) {
	h.log.Warn(msg, "err", err)
	writeError(w, http.StatusInternalServerError, msg)
}

func decodeBody(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(v)
}

// ---- health ----------------------------------------------------------------

func (h *Hub) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- nodes -----------------------------------------------------------------

// nodesVisibleTo returns the node list for one user: every node for admins,
// only bound nodes for anyone else. It backs GET /api/nodes and the /ws/web
// hello frame so both report the same view.
func (h *Hub) nodesVisibleTo(ctx context.Context, user *store.User) ([]protocol.Node, error) {
	q := h.store.Queries()
	var (
		nodes []protocol.Node
		err   error
	)
	if user.Role == "admin" {
		nodes, err = q.ListNodes(ctx)
	} else {
		// Non-admins discover only the nodes they are bound to.
		nodes, err = q.ListNodesByUser(ctx, user.ID)
	}
	if err != nil {
		return nil, err
	}
	if nodes == nil {
		nodes = []protocol.Node{}
	}
	return nodes, nil
}

func (h *Hub) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.nodesVisibleTo(r.Context(), userOf(r))
	if err != nil {
		h.serverError(w, "list nodes", err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// handleGetNode serves GET /api/nodes/{id}: the single-node lookup the web UI
// deep links (/nodes/:nodeId) rely on. Admins see every node; non-admins only
// their bound nodes (unbound -> 404, no existence leak).
func (h *Hub) handleGetNode(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeVisibleTo(r.Context(), userOf(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (h *Hub) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"name"`
		Host         string   `json:"host"`
		Capabilities []string `json:"capabilities"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// A missing `capabilities` field decodes to nil; the column is NOT NULL,
	// so normalize rather than 500 on a nil array parameter.
	if req.Capabilities == nil {
		req.Capabilities = []string{}
	}
	node, err := h.store.Queries().CreateNode(r.Context(), store.CreateNodeParams{
		Name:         req.Name,
		Host:         req.Host,
		Status:       protocol.NodeOffline,
		Capabilities: req.Capabilities,
		TokenHash:    "",
	})
	if err != nil {
		h.serverError(w, "create node", err)
		return
	}
	writeJSON(w, http.StatusCreated, node)
}

// ---- workspaces ------------------------------------------------------------

func (h *Hub) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	ctx := r.Context()
	// An unbound node is invisible: 404, never an empty list (no existence
	// leak). A binding covers the node's whole subtree.
	if ok, err := h.canReadNode(ctx, userOf(r), nodeID); err != nil || !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	workspaces, err := h.store.Queries().ListWorkspacesByNode(ctx, nodeID)
	if err != nil {
		h.serverError(w, "list workspaces", err)
		return
	}
	writeJSON(w, http.StatusOK, workspaces)
}

func (h *Hub) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
		Name   string `json:"name"`
		Path   string `json:"path"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NodeID == "" || req.Name == "" || req.Path == "" {
		writeError(w, http.StatusBadRequest, "node_id, name and path are required")
		return
	}

	// Admins may create anywhere; other users only on nodes they are bound
	// to (unauthorized write -> 403).
	ctx := r.Context()
	if ok, err := h.canReadNode(ctx, userOf(r), req.NodeID); err != nil || !ok {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	workspace, err := h.store.Queries().CreateWorkspace(ctx, store.CreateWorkspaceParams{
		NodeID: req.NodeID,
		Name:   req.Name,
		Path:   req.Path,
	})
	if err != nil {
		h.serverError(w, "create workspace", err)
		return
	}
	writeJSON(w, http.StatusCreated, workspace)
}

// handleGetWorkspace returns one workspace by id — the workspace page header
// resolves its name/path through this. Visibility is the node-binding rule
// via requireWorkspaceAccess (invisible -> 404).
func (h *Hub) handleGetWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := h.store.Queries().GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, workspace)
}

// ---- tasks -----------------------------------------------------------------

func (h *Hub) handleListTasks(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	nodeID := r.URL.Query().Get("node_id")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	switch {
	case workspaceID == "" && nodeID == "":
		writeError(w, http.StatusBadRequest, "workspace_id or node_id is required")
		return
	case workspaceID != "" && nodeID != "":
		writeError(w, http.StatusBadRequest, "provide only one of workspace_id or node_id")
		return
	}

	// Non-admins reach tasks only through the node binding (subtree
	// inheritance): an unbound workspace or node is invisible -> 404. A
	// binding covers every workspace beneath the node.
	ctx := r.Context()
	user := userOf(r)
	if user.Role != "admin" {
		var (
			ok  bool
			err error
		)
		if workspaceID != "" {
			ok, err = h.canReadWorkspace(ctx, user, workspaceID)
		} else {
			ok, err = h.canReadNode(ctx, user, nodeID)
		}
		if err != nil || !ok {
			if workspaceID != "" {
				writeError(w, http.StatusNotFound, "workspace not found")
			} else {
				writeError(w, http.StatusNotFound, "node not found")
			}
			return
		}
	}

	sq := h.store.Queries()
	var (
		tasks []protocol.Task
		err   error
	)
	switch {
	case workspaceID != "" && q != "":
		tasks, err = sq.SearchTasksByWorkspace(ctx, workspaceID, q)
	case workspaceID != "":
		// Board reads are cached per workspace and invalidated on every task
		// mutation (see invalidateTasks). Fuzzy search stays uncached.
		cacheKey := "tasks:ws:" + workspaceID
		var cached []protocol.Task
		if h.cachedJSON(ctx, cacheKey, &cached) {
			tasks = cached
		} else {
			tasks, err = sq.ListTasksByWorkspace(ctx, workspaceID)
			if err == nil {
				h.cacheSet(ctx, cacheKey, tasks, taskListTTL)
			}
		}
	case q != "":
		tasks, err = sq.SearchTasksByNode(ctx, nodeID, q)
	default:
		tasks, err = sq.ListTasksByNode(ctx, nodeID)
	}
	if err != nil {
		h.serverError(w, "list tasks", err)
		return
	}
	if tasks == nil {
		tasks = []protocol.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Hub) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Title       string `json:"title"`
		Prompt      string `json:"prompt"`
		AgentType   string `json:"agent_type"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.WorkspaceID == "" || req.Title == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and title are required")
		return
	}
	if req.AgentType == "" {
		req.AgentType = "fake"
	}

	ctx := r.Context()
	if ok, err := h.canWriteWorkspace(ctx, userOf(r), req.WorkspaceID); err != nil || !ok {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	task, err := h.store.Queries().CreateTask(ctx, store.CreateTaskParams{
		WorkspaceID: req.WorkspaceID,
		Title:       req.Title,
		Prompt:      req.Prompt,
		Status:      protocol.TaskWaiting,
		AgentType:   req.AgentType,
		SortOrder:   0,
	})
	if err != nil {
		h.serverError(w, "create task", err)
		return
	}
	h.invalidateTasks(ctx, req.WorkspaceID)
	h.bus.Publish(protocol.Event{Type: protocol.EvTaskCreated, Data: protocol.TaskCreated{Task: task}})

	dispatched, err := h.dispatchTask(ctx, task)
	if err != nil {
		// The task still exists; report the dispatch failure in the log and
		// return the task as created. Phase 2 keeps dispatch best-effort.
		h.log.Warn("dispatch task failed", "task_id", task.ID, "err", err)
		dispatched = task
	}
	writeJSON(w, http.StatusCreated, dispatched)
}

func (h *Hub) handlePatchTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Status    *protocol.TaskStatus `json:"status"`
		SortOrder *int                 `json:"sort_order"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx := r.Context()
	task, err := h.taskVisibleTo(ctx, userOf(r), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if req.Status != nil {
		if !validTaskStatus(*req.Status) {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
		if !canTransitionTask(task.Status, *req.Status) {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("invalid task transition %s -> %s", task.Status, *req.Status))
			return
		}
		updated, err := h.store.Queries().UpdateTaskStatus(ctx, id, *req.Status)
		if err != nil {
			h.serverError(w, "update task status", err)
			return
		}
		if updated.Status != task.Status {
			h.bus.Publish(protocol.Event{
				Type: protocol.EvTaskStatusChanged,
				Data: protocol.TaskStatusChanged{TaskID: id, From: task.Status, To: updated.Status},
			})
		}
		task = updated
	}

	if req.SortOrder != nil {
		if err := h.store.Queries().UpdateTaskSortOrder(ctx, id, *req.SortOrder); err != nil {
			h.serverError(w, "update task sort order", err)
			return
		}
		task.SortOrder = *req.SortOrder
	}

	h.invalidateTasks(ctx, task.WorkspaceID)
	writeJSON(w, http.StatusOK, task)
}

func validTaskStatus(s protocol.TaskStatus) bool {
	switch s {
	case protocol.TaskWaiting, protocol.TaskRunning, protocol.TaskDone, protocol.TaskAwaitingUser:
		return true
	default:
		return false
	}
}

// canTransitionTask is the manual (PATCH) transition table for the 4-state
// machine: done is terminal, same-status writes are idempotent, and everything
// else follows the lifecycle. Internal session->task derivation (moveTaskTo)
// owns its own mapping and does not go through here.
func canTransitionTask(from, to protocol.TaskStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case protocol.TaskWaiting:
		return to == protocol.TaskRunning || to == protocol.TaskAwaitingUser || to == protocol.TaskDone
	case protocol.TaskRunning:
		return to == protocol.TaskAwaitingUser || to == protocol.TaskDone
	case protocol.TaskAwaitingUser:
		return to == protocol.TaskRunning || to == protocol.TaskDone
	default:
		return false
	}
}

// ---- subtasks --------------------------------------------------------------

// handleListSubtasksByTask returns a task's mirrored todo items. Subtasks are
// written by the node's todo.update mirror; this is the read side the MCP
// server (fleetdeck_subtask_list) consumes.
func (h *Hub) handleListSubtasksByTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var uuid pgtype.UUID
	if err := uuid.Scan(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}

	if _, err := h.taskVisibleTo(r.Context(), userOf(r), id); err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	subtasks, err := h.store.Queries().ListSubtasksByTask(r.Context(), id)
	if err != nil {
		// A task with no subtasks yields an empty slice, not ErrNoRows, but
		// handle it defensively so the contract stays {"subtasks":[]}.
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"subtasks": []protocol.Subtask{}})
			return
		}
		h.serverError(w, "list subtasks", err)
		return
	}
	if subtasks == nil {
		subtasks = []protocol.Subtask{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subtasks": subtasks})
}

// ---- session history / gap-fill -------------------------------------------

func (h *Hub) handleListMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	limit := intQuery(r, "limit", 100)

	if _, err := h.sessionVisibleTo(r.Context(), userOf(r), sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	messages, err := h.store.Queries().ListMessagesBySession(r.Context(), sessionID, limit)
	if err != nil {
		h.serverError(w, "list messages", err)
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// eventDTO is store.Event shaped for the wire: Payload is emitted as raw JSON
// (the stored JSONB payload) instead of Go's default base64 for []byte.
type eventDTO struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Seq       int64           `json:"seq"`
	Ts        time.Time       `json:"ts"`
}

func (h *Hub) handleListEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	afterSeq := int64(intQuery(r, "after_seq", 0))
	limit := intQuery(r, "limit", 200)

	if _, err := h.sessionVisibleTo(r.Context(), userOf(r), sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	events, err := h.store.Queries().ListEventsBySession(r.Context(), sessionID, afterSeq, limit)
	if err != nil {
		h.serverError(w, "list events", err)
		return
	}
	out := make([]eventDTO, 0, len(events))
	for _, e := range events {
		payload := e.Payload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		out = append(out, eventDTO{
			ID:        e.ID,
			SessionID: e.SessionID,
			Type:      e.Type,
			Payload:   json.RawMessage(payload),
			Seq:       e.Seq,
			Ts:        e.Ts,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- session send ----------------------------------------------------------

// sendMessageRequest is the JSON body of POST /api/sessions/{id}/send.
type sendMessageRequest struct {
	Message string `json:"message"`
}

// handleSendMessage records a user reply to an awaiting_user session, flips the
// session back to running, resumes the parent task in lock-step, broadcasts all
// changes to web subscribers, and forwards the reply to the owning node. It is
// best-effort with respect to the node: if the node is offline the message is
// still stored and the caller gets 202 instead of an error.
func (h *Hub) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req sendMessageRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	msgText := strings.TrimSpace(req.Message)
	if msgText == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	ctx := r.Context()
	session, err := h.sessionVisibleTo(ctx, userOf(r), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if session.Status != protocol.SessionAwaitingUser {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "session is not awaiting user input",
			"status": string(session.Status),
		})
		return
	}

	// Persist the user message and resume the session atomically.
	var inserted protocol.Message
	if err := h.store.WithTx(ctx, func(q *store.Queries) error {
		msg, err := q.CreateMessage(ctx, store.CreateMessageParams{
			SessionID: id,
			Role:      protocol.RoleUser,
			Content:   msgText,
		})
		if err != nil {
			return err
		}
		inserted = msg
		return q.UpdateSessionStatus(ctx, id, protocol.SessionRunning, nil)
	}); err != nil {
		h.serverError(w, "send message", err)
		return
	}

	// Locked decision: resume the parent task in lock-step with the session so
	// the board clears "waiting for user" immediately.
	h.resumeTaskOnReply(ctx, id)

	// Broadcast only after the transaction commits.
	h.bus.Publish(protocol.Event{
		Type: protocol.EvSessionMessage,
		Data: protocol.SessionMessage{SessionID: id, Message: inserted},
	})
	h.bus.Publish(protocol.Event{
		Type: protocol.EvSessionStateChanged,
		Data: protocol.SessionStateChanged{SessionID: id, Status: protocol.SessionRunning},
	})

	// Forward the reply to the node that owns the session. An offline node is
	// not a failure: the message is already durable and will be replayed/picked
	// up on reconnect.
	env := protocol.NewEnvelope(protocol.MsgSendMessage, protocol.SendMessageCommand{
		SessionID: id,
		Message:   msgText,
	})
	if !h.sendToNode(session.NodeID, env) {
		h.log.Warn("could not deliver session.send_message to node",
			"session_id", id, "node_id", session.NodeID)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status":  "queued",
			"warning": "node offline; will not run until reconnect",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": inserted})
}

// ---- session snapshot ------------------------------------------------------

// sessionSnapshot is the body of GET /api/sessions/{id}: the session plus its
// owning task. Task is a pointer so an unresolvable parent renders as JSON null,
// matching the web's SessionSnapshot type ({ session, task: Task | null }).
type sessionSnapshot struct {
	Session protocol.Session `json:"session"`
	Task    *protocol.Task   `json:"task"`
}

// handleGetSession returns a session and the task it belongs to in one round
// trip, backing the web's getSession(id) call.
func (h *Hub) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	ctx := r.Context()
	session, err := h.sessionVisibleTo(ctx, userOf(r), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	snapshot := sessionSnapshot{Session: session}
	// The parent task should always exist (session.task_id is a FK), but resolve
	// defensively: an unresolvable task yields JSON null rather than an error.
	if task, err := h.store.Queries().GetTask(ctx, session.TaskID); err == nil {
		snapshot.Task = &task
	}

	writeJSON(w, http.StatusOK, snapshot)
}

func intQuery(r *http.Request, key string, fallback int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
