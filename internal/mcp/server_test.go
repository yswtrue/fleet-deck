package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"fleetdeck/internal/protocol"
)

// ---- fixtures --------------------------------------------------------------

const (
	fixtureSessionID = "sess-1"
	fixtureTaskID    = "task-1"
	fixtureWSID      = "ws-1"
	fixtureNodeID    = "node-1"
)

var (
	fixtureTime = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

	fixtureSession = protocol.Session{
		ID:        fixtureSessionID,
		TaskID:    fixtureTaskID,
		NodeID:    fixtureNodeID,
		AgentType: "fake",
		Status:    protocol.SessionAwaitingUser,
		CreatedAt: fixtureTime,
	}
	fixtureTask = protocol.Task{
		ID:          fixtureTaskID,
		WorkspaceID: fixtureWSID,
		Title:       "Fix the bug",
		Status:      protocol.TaskAwaitingUser,
		AgentType:   "fake",
		SessionID:   fixtureSessionID,
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime,
	}
	fixtureTask2 = protocol.Task{
		ID:          "task-2",
		WorkspaceID: fixtureWSID,
		Title:       "Ship the fix",
		Status:      protocol.TaskDone,
		AgentType:   "fake",
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime,
	}
	fixtureTask3 = protocol.Task{
		ID:          "task-3",
		WorkspaceID: "ws-2",
		Title:       "Other node task",
		Status:      protocol.TaskRunning,
		AgentType:   "fake",
		SessionID:   "sess-3",
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime,
	}
	fixtureWorkspaces = []protocol.Workspace{
		{ID: fixtureWSID, NodeID: fixtureNodeID, Name: "fleet-deck", Path: "/code/fleet-deck", CreatedAt: fixtureTime},
		{ID: "ws-2", NodeID: fixtureNodeID, Name: "other", Path: "/code/other", CreatedAt: fixtureTime},
	}
	fixtureSubtasks = []protocol.Subtask{
		{ID: "sub-1", TaskID: fixtureTaskID, Title: "Research", Status: protocol.SubtaskDone, ExternalID: "t1", SortOrder: 0},
		{ID: "sub-2", TaskID: fixtureTaskID, Title: "Implement", Status: protocol.SubtaskInProgress, ExternalID: "t2", SortOrder: 1},
	}
	fixtureMessages = []protocol.Message{
		{ID: "msg-1", SessionID: fixtureSessionID, Role: protocol.RoleUser, Content: "please fix", CreatedAt: fixtureTime},
		{ID: "msg-2", SessionID: fixtureSessionID, Role: protocol.RoleAssistant, Content: "on it", CreatedAt: fixtureTime.Add(time.Second)},
	}
	fixtureEvents = []Event{
		{ID: "ev-1", SessionID: fixtureSessionID, Type: "session.state_changed", Payload: json.RawMessage(`{"status":"running"}`), Seq: 1, Ts: fixtureTime},
		{ID: "ev-2", SessionID: fixtureSessionID, Type: "todo.update", Payload: json.RawMessage(`{"todos":[]}`), Seq: 2, Ts: fixtureTime.Add(time.Second)},
	}
)

// ---- stub hub --------------------------------------------------------------

// stubHub is an httptest-backed stand-in for the hub's REST API. It records
// every request so tests can assert routing.
type stubHub struct {
	mu       sync.Mutex
	requests []string
	queued   bool
}

func (s *stubHub) record(r *http.Request) {
	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	s.mu.Lock()
	s.requests = append(s.requests, entry)
	s.mu.Unlock()
}

func (s *stubHub) sawRequest(want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, got := range s.requests {
		if got == want {
			return true
		}
	}
	return false
}

func (s *stubHub) sawPrefix(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, got := range s.requests {
		if strings.HasPrefix(got, prefix) {
			return true
		}
	}
	return false
}

func (s *stubHub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *stubHub) setQueued(v bool) {
	s.mu.Lock()
	s.queued = v
	s.mu.Unlock()
}

func (s *stubHub) isQueued() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queued
}

func writeStubJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newStubHub(t *testing.T) (*stubHub, *httptest.Server) {
	t.Helper()
	s := &stubHub{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		switch r.PathValue("id") {
		case "missing":
			writeStubJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		case "boom":
			writeStubJSON(w, http.StatusInternalServerError, map[string]string{"error": "boom"})
		default:
			writeStubJSON(w, http.StatusOK, SessionSnapshot{Session: fixtureSession, Task: &fixtureTask})
		}
	})

	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		switch r.URL.Query().Get("workspace_id") {
		case fixtureWSID:
			writeStubJSON(w, http.StatusOK, []protocol.Task{fixtureTask, fixtureTask2})
		case "ws-2":
			writeStubJSON(w, http.StatusOK, []protocol.Task{fixtureTask3})
		case "":
			// Mirror the hub's filter contract: node_id alone spans the node.
			if r.URL.Query().Get("node_id") == fixtureNodeID {
				writeStubJSON(w, http.StatusOK, []protocol.Task{fixtureTask, fixtureTask2, fixtureTask3})
				return
			}
			writeStubJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id or node_id is required"})
		default:
			writeStubJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id or node_id is required"})
		}
	})

	mux.HandleFunc("GET /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if r.URL.Query().Get("node_id") != fixtureNodeID {
			writeStubJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
			return
		}
		writeStubJSON(w, http.StatusOK, fixtureWorkspaces)
	})

	mux.HandleFunc("GET /api/tasks/{id}/subtasks", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		writeStubJSON(w, http.StatusOK, map[string]any{"subtasks": fixtureSubtasks})
	})

	mux.HandleFunc("GET /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		writeStubJSON(w, http.StatusOK, fixtureMessages)
	})

	mux.HandleFunc("GET /api/sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		writeStubJSON(w, http.StatusOK, fixtureEvents)
	})

	mux.HandleFunc("POST /api/sessions/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if r.PathValue("id") == "conflict" {
			writeStubJSON(w, http.StatusConflict, map[string]string{"error": "session is not awaiting user input"})
			return
		}
		if s.isQueued() {
			writeStubJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "warning": "node offline"})
			return
		}
		writeStubJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"message": protocol.Message{
				ID:        "msg-3",
				SessionID: fixtureSessionID,
				Role:      protocol.RoleUser,
				Content:   "resume",
				CreatedAt: fixtureTime,
			},
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

// ---- harness ---------------------------------------------------------------

// newTestEnv wires a FleetDeck MCP server to the stub hub and connects a real
// MCP client over the SDK's in-memory transport, so tests exercise the full
// JSON-RPC round trip.
func newTestEnv(t *testing.T) (*stubHub, *mcp.ClientSession) {
	t.Helper()
	stub, ts := newStubHub(t)

	srv := NewServer(NewHubClient(ts.URL))
	ct, st := mcp.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = serverSession.Wait()
	})
	return stub, cs
}

func callTool[T any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("tool %s returned error result: %s", name, errorText(res))
	}
	return decodeStructured[T](t, res)
}

func callToolExpectError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("tool %s: expected an error result, got %#v", name, res.StructuredContent)
	}
	return errorText(res)
}

func decodeStructured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal structured content %s: %v", raw, err)
	}
	return out
}

func errorText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// ---- session_status --------------------------------------------------------

func TestSessionStatus(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[SessionStatusOutput](t, cs, "fleetdeck_session_status", map[string]any{"session_id": fixtureSessionID})

	if out.Session.ID != fixtureSessionID || out.Session.Status != "awaiting_user" || out.Session.TaskID != fixtureTaskID {
		t.Fatalf("unexpected session summary: %+v", out.Session)
	}
	if out.Task == nil {
		t.Fatal("expected a parent task")
	}
	if out.Task.ID != fixtureTaskID || out.Task.Status != "awaiting_user" {
		t.Fatalf("unexpected task summary: %+v", *out.Task)
	}
	if !stub.sawRequest("GET /api/sessions/" + fixtureSessionID) {
		t.Fatalf("hub did not receive session request; saw %v", stub.requests)
	}
}

func TestSessionStatusValidation(t *testing.T) {
	_, cs := newTestEnv(t)

	// A missing field is rejected by the SDK's input-schema validation before
	// the handler runs.
	if msg := callToolExpectError(t, cs, "fleetdeck_session_status", map[string]any{}); !strings.Contains(msg, "session_id") {
		t.Fatalf("unexpected schema error: %q", msg)
	}
	// A whitespace-only value passes schema validation and is caught in the handler.
	if msg := callToolExpectError(t, cs, "fleetdeck_session_status", map[string]any{"session_id": "   "}); !strings.Contains(msg, "session_id is required") {
		t.Fatalf("unexpected handler error: %q", msg)
	}
}

func TestSessionStatusNotFound(t *testing.T) {
	_, cs := newTestEnv(t)
	msg := callToolExpectError(t, cs, "fleetdeck_session_status", map[string]any{"session_id": "missing"})
	if !strings.Contains(msg, "404") {
		t.Fatalf("expected 404 to surface as a tool error, got %q", msg)
	}
}

func TestHubServerErrorPropagates(t *testing.T) {
	_, cs := newTestEnv(t)
	msg := callToolExpectError(t, cs, "fleetdeck_session_status", map[string]any{"session_id": "boom"})
	if !strings.Contains(msg, "500") {
		t.Fatalf("expected 500 to surface as a tool error, got %q", msg)
	}
}

// ---- task_list -------------------------------------------------------------

func TestTaskListByWorkspace(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[TaskListOutput](t, cs, "fleetdeck_task_list", map[string]any{"workspace_id": fixtureWSID})
	if len(out.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got %+v", out.Tasks)
	}
	if out.Tasks[0].ID != fixtureTaskID || out.Tasks[0].SessionID == nil || *out.Tasks[0].SessionID != fixtureSessionID {
		t.Fatalf("unexpected first task: %+v", out.Tasks[0])
	}
	if out.Tasks[1].SessionID != nil {
		t.Fatalf("task without a session must omit session_id, got %q", *out.Tasks[1].SessionID)
	}
	if !stub.sawRequest("GET /api/tasks?workspace_id=" + fixtureWSID) {
		t.Fatalf("hub did not receive the workspace task request; saw %v", stub.requests)
	}
}

func TestTaskListByNodeSingleRoundTrip(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[TaskListOutput](t, cs, "fleetdeck_task_list", map[string]any{"node_id": fixtureNodeID})
	if len(out.Tasks) != 3 {
		t.Fatalf("want 3 tasks across both workspaces, got %+v", out.Tasks)
	}
	// The old implementation walked every workspace (1 + N requests). The
	// collapse must be exactly one GET /api/tasks?node_id= and no workspace
	// listing at all.
	if !stub.sawRequest("GET /api/tasks?node_id=" + fixtureNodeID) {
		t.Fatalf("hub did not receive the by-node task request; saw %v", stub.requests)
	}
	if stub.sawPrefix("GET /api/workspaces") {
		t.Fatalf("task_list must not walk workspaces; saw %v", stub.requests)
	}
	if n := stub.requestCount(); n != 1 {
		t.Fatalf("task_list made %d hub requests, want 1: %v", n, stub.requests)
	}
}

func TestTaskListStatusFilter(t *testing.T) {
	_, cs := newTestEnv(t)

	out := callTool[TaskListOutput](t, cs, "fleetdeck_task_list", map[string]any{
		"workspace_id": fixtureWSID,
		"status":       "done",
	})
	if len(out.Tasks) != 1 || out.Tasks[0].ID != "task-2" {
		t.Fatalf("want only the done task, got %+v", out.Tasks)
	}
}

func TestTaskListValidation(t *testing.T) {
	_, cs := newTestEnv(t)
	msg := callToolExpectError(t, cs, "fleetdeck_task_list", map[string]any{})
	if !strings.Contains(msg, "workspace_id or node_id is required") {
		t.Fatalf("unexpected error: %q", msg)
	}
}

func TestTaskListInvalidStatus(t *testing.T) {
	_, cs := newTestEnv(t)
	msg := callToolExpectError(t, cs, "fleetdeck_task_list", map[string]any{
		"workspace_id": fixtureWSID,
		"status":       "bogus",
	})
	if !strings.Contains(msg, "invalid status") {
		t.Fatalf("unexpected error: %q", msg)
	}
}

// ---- subtask_list ----------------------------------------------------------

func TestSubtaskList(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[SubtaskListOutput](t, cs, "fleetdeck_subtask_list", map[string]any{"task_id": fixtureTaskID})
	if len(out.Subtasks) != 2 {
		t.Fatalf("want 2 subtasks, got %+v", out.Subtasks)
	}
	if out.Subtasks[0].ExternalID != "t1" || out.Subtasks[1].Status != "in_progress" {
		t.Fatalf("unexpected subtasks: %+v", out.Subtasks)
	}
	if !stub.sawRequest("GET /api/tasks/" + fixtureTaskID + "/subtasks") {
		t.Fatalf("hub did not receive the subtask request; saw %v", stub.requests)
	}
}

func TestSubtaskListValidation(t *testing.T) {
	_, cs := newTestEnv(t)
	if msg := callToolExpectError(t, cs, "fleetdeck_subtask_list", map[string]any{}); !strings.Contains(msg, "task_id") {
		t.Fatalf("unexpected schema error: %q", msg)
	}
	if msg := callToolExpectError(t, cs, "fleetdeck_subtask_list", map[string]any{"task_id": "  "}); !strings.Contains(msg, "task_id is required") {
		t.Fatalf("unexpected handler error: %q", msg)
	}
}

// ---- message_tail ----------------------------------------------------------

func TestMessageTail(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[MessageTailOutput](t, cs, "fleetdeck_message_tail", map[string]any{
		"session_id": fixtureSessionID,
		"limit":      5,
	})
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 messages, got %+v", out.Messages)
	}
	if out.Messages[0].Role != "user" || out.Messages[1].Role != "assistant" {
		t.Fatalf("unexpected roles: %+v", out.Messages)
	}
	if out.Messages[0].CreatedAt == "" {
		t.Fatal("expected created_at to be populated")
	}
	if !stub.sawRequest("GET /api/sessions/" + fixtureSessionID + "/messages?limit=5") {
		t.Fatalf("hub did not receive the message request; saw %v", stub.requests)
	}
}

func TestMessageTailDefaultsLimit(t *testing.T) {
	stub, cs := newTestEnv(t)

	_ = callTool[MessageTailOutput](t, cs, "fleetdeck_message_tail", map[string]any{"session_id": fixtureSessionID})
	if !stub.sawRequest("GET /api/sessions/" + fixtureSessionID + "/messages?limit=20") {
		t.Fatalf("expected default limit=20; saw %v", stub.requests)
	}
}

// ---- event_tail ------------------------------------------------------------

func TestEventTail(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[EventTailOutput](t, cs, "fleetdeck_event_tail", map[string]any{
		"session_id": fixtureSessionID,
		"after_seq":  1,
	})
	if len(out.Events) != 2 {
		t.Fatalf("want 2 events, got %+v", out.Events)
	}
	if out.Events[1].Seq != 2 || out.Events[1].Type != "todo.update" {
		t.Fatalf("unexpected second event: %+v", out.Events[1])
	}
	if out.Events[0].Data["status"] != "running" {
		t.Fatalf("expected payload decoded into data, got %+v", out.Events[0].Data)
	}
	if !stub.sawPrefix("GET /api/sessions/" + fixtureSessionID + "/events?") {
		t.Fatalf("hub did not receive the events request; saw %v", stub.requests)
	}
	if !stub.sawRequest("GET /api/sessions/" + fixtureSessionID + "/events?after_seq=1&limit=100") {
		t.Fatalf("expected after_seq and default limit forwarded; saw %v", stub.requests)
	}
}

// ---- send_message ----------------------------------------------------------

func TestSendMessageDelivered(t *testing.T) {
	stub, cs := newTestEnv(t)

	out := callTool[SendMessageOutput](t, cs, "fleetdeck_session_send_message", map[string]any{
		"session_id": fixtureSessionID,
		"message":    "resume please",
	})
	if !out.OK || out.Status != "running" {
		t.Fatalf("unexpected send result: %+v", out)
	}
	if !stub.sawRequest("POST /api/sessions/" + fixtureSessionID + "/send") {
		t.Fatalf("hub did not receive the send request; saw %v", stub.requests)
	}
}

func TestSendMessageQueuedWhenNodeOffline(t *testing.T) {
	stub, cs := newTestEnv(t)
	stub.setQueued(true)

	out := callTool[SendMessageOutput](t, cs, "fleetdeck_session_send_message", map[string]any{
		"session_id": fixtureSessionID,
		"message":    "resume please",
	})
	if !out.OK || out.Status != "queued" {
		t.Fatalf("expected a queued result, got %+v", out)
	}
}

func TestSendMessageHubError(t *testing.T) {
	_, cs := newTestEnv(t)
	msg := callToolExpectError(t, cs, "fleetdeck_session_send_message", map[string]any{
		"session_id": "conflict",
		"message":    "resume please",
	})
	if !strings.Contains(msg, "409") {
		t.Fatalf("expected 409 to surface as a tool error, got %q", msg)
	}
}

func TestSendMessageValidation(t *testing.T) {
	_, cs := newTestEnv(t)

	// Missing fields are caught by the SDK's schema validation.
	if msg := callToolExpectError(t, cs, "fleetdeck_session_send_message", map[string]any{"message": "hi"}); !strings.Contains(msg, "session_id") {
		t.Fatalf("unexpected schema error: %q", msg)
	}
	if msg := callToolExpectError(t, cs, "fleetdeck_session_send_message", map[string]any{"session_id": fixtureSessionID}); !strings.Contains(msg, "message") {
		t.Fatalf("unexpected schema error: %q", msg)
	}
	// Whitespace-only values pass schema validation and are caught in the handler.
	if msg := callToolExpectError(t, cs, "fleetdeck_session_send_message", map[string]any{"session_id": "  ", "message": "hi"}); !strings.Contains(msg, "session_id is required") {
		t.Fatalf("unexpected handler error: %q", msg)
	}
	if msg := callToolExpectError(t, cs, "fleetdeck_session_send_message", map[string]any{"session_id": fixtureSessionID, "message": "   "}); !strings.Contains(msg, "message is required") {
		t.Fatalf("unexpected handler error: %q", msg)
	}
}

// ---- end-to-end ------------------------------------------------------------

// TestServerEndToEnd lists the tool surface and invokes every tool through a
// real in-memory MCP session.
func TestServerEndToEnd(t *testing.T) {
	_, cs := newTestEnv(t)

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{
		"fleetdeck_session_status",
		"fleetdeck_task_list",
		"fleetdeck_subtask_list",
		"fleetdeck_message_tail",
		"fleetdeck_event_tail",
		"fleetdeck_session_send_message",
	}
	if len(list.Tools) != len(want) {
		t.Fatalf("want %d tools, got %d: %v", len(want), len(list.Tools), toolNames(list.Tools))
	}
	got := map[string]bool{}
	for _, tool := range list.Tools {
		got[tool.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q in %v", name, toolNames(list.Tools))
		}
	}

	status := callTool[SessionStatusOutput](t, cs, "fleetdeck_session_status", map[string]any{"session_id": fixtureSessionID})
	if status.Session.ID != fixtureSessionID {
		t.Errorf("session_status: unexpected session %+v", status.Session)
	}

	tasks := callTool[TaskListOutput](t, cs, "fleetdeck_task_list", map[string]any{"workspace_id": fixtureWSID})
	if len(tasks.Tasks) != 2 {
		t.Errorf("task_list: want 2 tasks, got %+v", tasks.Tasks)
	}

	subtasks := callTool[SubtaskListOutput](t, cs, "fleetdeck_subtask_list", map[string]any{"task_id": fixtureTaskID})
	if len(subtasks.Subtasks) != 2 {
		t.Errorf("subtask_list: want 2 subtasks, got %+v", subtasks.Subtasks)
	}

	messages := callTool[MessageTailOutput](t, cs, "fleetdeck_message_tail", map[string]any{"session_id": fixtureSessionID})
	if len(messages.Messages) != 2 {
		t.Errorf("message_tail: want 2 messages, got %+v", messages.Messages)
	}

	events := callTool[EventTailOutput](t, cs, "fleetdeck_event_tail", map[string]any{"session_id": fixtureSessionID})
	if len(events.Events) != 2 {
		t.Errorf("event_tail: want 2 events, got %+v", events.Events)
	}

	sent := callTool[SendMessageOutput](t, cs, "fleetdeck_session_send_message", map[string]any{
		"session_id": fixtureSessionID,
		"message":    "resume",
	})
	if !sent.OK {
		t.Errorf("send_message: unexpected result %+v", sent)
	}
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
