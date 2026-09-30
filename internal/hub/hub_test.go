package hub

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// testDSN returns FLEETDECK_DATABASE_URL for live-DB tests. Credentials are
// env-only (no baked-in DSN): the caller's test skips when it is unset.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FLEETDECK_DATABASE_URL")
	if dsn == "" {
		t.Skip("FLEETDECK_DATABASE_URL not set; live-DB test skipped")
	}
	return dsn
}

// newTestHub starts a hub backed by the live dev Postgres and an httptest
// server, skipping when Postgres is unreachable. It also provisions an admin
// user and bearer token (testAuth) so every test starts authenticated;
// pass auth.token to the HTTP helpers.
func newTestHub(t *testing.T) (*Hub, *store.Store, *httptest.Server, testAuth) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	st, err := store.New(ctx, testDSN(t))
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	t.Cleanup(st.Close)

	// Every hub test runs through a real Redis-backed cache (miniredis), so
	// caching and invalidation bugs surface across the whole suite.
	mr := miniredis.RunT(t)
	c, err := cache.NewRedis("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("test cache: %v", err)
	}

	h := New(ctx, st, adapters.NewRegistry(), c)
	t.Cleanup(func() { _ = h.Close() })

	ts := httptest.NewServer(h.Handler())
	t.Cleanup(ts.Close)

	user, token := setupUserWithRole(t, st, "admin")
	return h, st, ts, testAuth{user: user, token: token}
}

// dialNode registers a scripted node over the real /ws/node endpoint and
// returns its connection plus the hub-issued node id. The returned node does
// not read commands unless the caller does.
func dialNode(t *testing.T, ts *httptest.Server, st *store.Store, name string) (*websocket.Conn, string) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}

	regEnv := protocol.NewEnvelope(protocol.MsgRegisterRequest, protocol.RegisterRequest{
		Name:         name,
		Host:         "test-host",
		Capabilities: []string{"fake"},
	})
	if err := c.WriteJSON(regEnv); err != nil {
		t.Fatalf("send register: %v", err)
	}
	var respEnv protocol.Envelope
	if err := c.ReadJSON(&respEnv); err != nil {
		t.Fatalf("read register response: %v", err)
	}
	if respEnv.Type != protocol.MsgRegisterResponse {
		t.Fatalf("first reply type = %q, want %q", respEnv.Type, protocol.MsgRegisterResponse)
	}
	var regResp protocol.RegisterResponse
	if err := respEnv.Decode(&regResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if regResp.NodeID == "" {
		t.Fatal("register response had empty node_id")
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", regResp.NodeID); err != nil {
			t.Logf("cleanup: delete node: %v", err)
		}
	})
	return c, regResp.NodeID
}

func uniqueName(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func createWorkspaceAndTask(t *testing.T, token string, ts *httptest.Server, nodeID string) protocol.Task {
	t.Helper()
	workspace := postJSON[protocol.Workspace](t, token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID,
		"name":    uniqueName("it-ws"),
		"path":    "/tmp/it-ws",
	})
	return postJSON[protocol.Task](t, token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": workspace.ID,
		"title":        uniqueName("it-task"),
		"prompt":       "do the thing",
		"agent_type":   "fake",
	})
}

// TestNodeRegistrationDrivesTaskToDone wires a real hub to a scripted node
// WebSocket and asserts the full loop: registration, task dispatch, todo
// mirroring, and the waiting->running->done transition.
func TestNodeRegistrationDrivesTaskToDone(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-it-node"))
	defer func() { _ = c.Close() }()

	// Scripted node: on task.start, mirror one todo and report done.
	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgTaskStart {
				continue
			}
			var cmd protocol.TaskStartCommand
			if err := env.Decode(&cmd); err != nil {
				continue
			}
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgTodoUpdate, protocol.TodoUpdate{
				SessionID: cmd.SessionID,
				Todos: []protocol.Todo{
					{ID: "1", Title: "Research", Status: protocol.SubtaskDone},
				},
			}))
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
				Type: protocol.EvSessionStateChanged,
				Data: protocol.SessionStateChanged{SessionID: cmd.SessionID, Status: protocol.SessionDone},
				Ts:   time.Now().UTC(),
			}))
		}
	}()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.Status != protocol.TaskRunning {
		t.Fatalf("task status right after create = %q, want %q", task.Status, protocol.TaskRunning)
	}

	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	var last protocol.Task
	for {
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil {
			last = got
			if got.Status == protocol.TaskDone {
				subs, serr := st.Queries().ListSubtasksByTask(ctx, task.ID)
				if serr == nil && len(subs) >= 1 && subs[0].ExternalID == "1" {
					return // success
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach done with a subtask in time; last status=%q", last.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestNodeOfflineCascadesSessions asserts that losing a node fails its
// non-terminal sessions and completes their tasks, broadcasting both changes.
func TestNodeOfflineCascadesSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("offline-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" || task.Status != protocol.TaskRunning {
		t.Fatalf("task not dispatched: status=%q session=%q", task.Status, task.SessionID)
	}

	events, unsubscribe := h.Bus().Subscribe(EventScope{All: true})
	defer unsubscribe()

	// The node disappears without reporting completion.
	_ = c.Close()

	var sawSessionFailed, sawTaskDone bool
	timeout := time.After(3 * time.Second)
	for !sawSessionFailed || !sawTaskDone {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("broadcaster closed unexpectedly")
			}
			if evt.Type == protocol.EvSessionStateChanged {
				if sc, ok := evt.Data.(protocol.SessionStateChanged); ok &&
					sc.SessionID == task.SessionID && sc.Status == protocol.SessionFailed {
					sawSessionFailed = true
				}
			}
			if evt.Type == protocol.EvTaskStatusChanged {
				if tc, ok := evt.Data.(protocol.TaskStatusChanged); ok &&
					tc.TaskID == task.ID && tc.To == protocol.TaskDone {
					sawTaskDone = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for cascade: sawSessionFailed=%v sawTaskDone=%v", sawSessionFailed, sawTaskDone)
		}
	}

	ctx := context.Background()
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionFailed {
		t.Fatalf("session status = %q (err %v), want failed", sess.Status, err)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil || got.Status != protocol.TaskDone {
		t.Fatalf("task status = %q (err %v), want done", got.Status, err)
	}
}

// TestEvSessionMessageBuffers posts a node session.event and asserts the
// storage policy: the message is buffered in the hub's ephemeral text state
// (served by GET /api/sessions/{id}/messages while the session is live) and no
// content-shaped event is persisted to the server-side event stream.
func TestEvSessionMessageBuffers(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("msg-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	content := uniqueName("hello-from-node")
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: task.SessionID,
		Message: protocol.Message{
			ID:        "m1",
			SessionID: task.SessionID,
			Role:      protocol.RoleAssistant,
			Content:   content,
			CreatedAt: time.Now().UTC(),
		},
	})); err != nil {
		t.Fatalf("send session.event: %v", err)
	}

	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	for {
		buf := h.textBuffer(ctx, task.SessionID)
		for _, m := range buf.Messages {
			if m.Content == content && m.Role == protocol.RoleAssistant {
				// The status-event stream must stay content-free.
				events, err := st.Queries().ListEventsBySession(ctx, task.SessionID, 0, 50)
				if err != nil {
					t.Fatalf("list events: %v", err)
				}
				for _, e := range events {
					if e.Type == protocol.EvSessionMessage || e.Type == protocol.EvSessionDelta {
						t.Fatalf("content event %q persisted to the event stream", e.Type)
					}
				}
				return // buffered, nothing persisted
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session message was not buffered (%d buffered)", len(buf.Messages))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func postJSON[T any](t *testing.T, token, url string, body any) T {
	t.Helper()
	resp, raw := postRaw(t, token, url, body)
	return decodeOK[T](t, url, resp, raw)
}
