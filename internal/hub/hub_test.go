package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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

// testJoinToken is the deployment-level pairing secret newTestHub installs, so
// every scripted node handshake in the suite is authorized.
const testJoinToken = "test-join-token"

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

	// Nodes must present a pairing credential. Tests use the deployment-level
	// secret so dialNode needs no per-test token row; the single-use database
	// tokens have their own tests in join_token_test.go.
	if os.Getenv("FLEETDECK_NODE_JOIN_TOKEN") == "" {
		t.Setenv("FLEETDECK_NODE_JOIN_TOKEN", testJoinToken)
	}

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

	// Production debounces the offline cascade by 30 s so a reconnect keeps its
	// sessions; tests that assert the cascade need it to fire promptly.
	h.offlineGrace = 50 * time.Millisecond

	user, token := setupUserWithRole(t, st, "admin")
	return h, st, ts, testAuth{user: user, token: token}
}

// seedNode inserts an offline node row directly. Node creation is not an HTTP
// action anymore: nodes claim themselves with a join token, so tests that need
// a pre-existing offline node (visibility, offline dispatch) seed the row.
func seedNode(t *testing.T, st *store.Store, name string) protocol.Node {
	t.Helper()
	node, err := st.Queries().CreateNode(context.Background(), store.CreateNodeParams{
		Name:      name,
		Host:      "test-host",
		Status:    protocol.NodeOffline,
		TokenHash: "",
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return node
}

// dialNode registers a scripted node over the real /ws/node endpoint and
// returns its connection plus the hub-issued node id. The returned node does
// not read commands unless the caller does.
func dialNode(t *testing.T, ts *httptest.Server, st *store.Store, name string) (*websocket.Conn, string) {
	t.Helper()
	c, resp := dialNodeAs(t, ts, st, name, "", testJoinToken)
	return c, resp.NodeID
}

// dialNodeAs registers a node connection. With an empty token the deployment
// join token is presented (first registration); a reconnect passes the token
// the hub issued on that first registration instead.
func dialNodeAs(t *testing.T, ts *httptest.Server, st *store.Store, name, token, joinToken string) (*websocket.Conn, protocol.RegisterResponse) {
	t.Helper()
	return dialNodeFull(t, ts, st, name, token, joinToken, nil)
}

// dialNodeFull registers a scripted node with an explicit live-session list.
// A nil liveSessions omits the field entirely (a daemon predating it); a
// current daemon always sends an array, empty when no adapter survived.
func dialNodeFull(t *testing.T, ts *httptest.Server, st *store.Store, name, token, joinToken string, liveSessions []string) (*websocket.Conn, protocol.RegisterResponse) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}

	req := protocol.RegisterRequest{
		Name:      name,
		Host:      "test-host",
		Token:     token,
		MachineID: "test-machine:" + name,
		Sessions:  liveSessions,
	}
	if token == "" {
		req.JoinToken = joinToken
	}
	regEnv := protocol.NewEnvelope(protocol.MsgRegisterRequest, req)
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
		t.Fatalf("register response had empty node_id (error %q)", regResp.Error)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", regResp.NodeID); err != nil {
			t.Logf("cleanup: delete node: %v", err)
		}
	})
	return c, regResp
}

func uniqueName(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// TestRegisterNodeTracksAgentTypes covers the picker's data source: the
// handshake's agent list is persisted, refreshed on reconnect, and preserved
// when an older daemon omits the field entirely.
func TestRegisterNodeTracksAgentTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, _, _ := newTestHub(t)
	ctx := context.Background()
	name := uniqueName("agents-node")
	machineID := "agents-machine:" + name
	agentTypes := []string{"codex", "omp"}

	node, token, err := h.registerNode(ctx, protocol.RegisterRequest{
		Name:           name,
		Host:           "test-host",
		AgentTypes:     agentTypes,
		DefaultAdapter: "omp",
		Version:        "v1.2.3",
		JoinToken:      testJoinToken,
		MachineID:      machineID,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID); err != nil {
			t.Logf("cleanup: delete node: %v", err)
		}
	})
	if !slices.Equal(node.AgentTypes, agentTypes) {
		t.Fatalf("registered agent_types = %v, want %v", node.AgentTypes, agentTypes)
	}
	if node.DefaultAdapter != "omp" {
		t.Fatalf("registered default_adapter = %q, want omp", node.DefaultAdapter)
	}
	if node.Version != "v1.2.3" {
		t.Fatalf("registered version = %q, want v1.2.3", node.Version)
	}

	// A reconnect with a changed list replaces the stored one (a newly
	// installed or removed agent must show up without re-pairing), and the
	// default travels with it.
	_, _, err = h.registerNode(ctx, protocol.RegisterRequest{
		Name:           name,
		Token:          token,
		AgentTypes:     []string{"codex"},
		DefaultAdapter: "codex",
		Version:        "v1.3.0",
		MachineID:      machineID,
	})
	if err != nil {
		t.Fatalf("reconnect node: %v", err)
	}
	stored, err := st.Queries().GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !slices.Equal(stored.AgentTypes, []string{"codex"}) {
		t.Fatalf("reconnected agent_types = %v, want [codex]", stored.AgentTypes)
	}
	if stored.DefaultAdapter != "codex" {
		t.Fatalf("reconnected default_adapter = %q, want codex", stored.DefaultAdapter)
	}
	if stored.Version != "v1.3.0" {
		t.Fatalf("reconnected version = %q, want v1.3.0", stored.Version)
	}

	// A daemon that predates the field sends nil; that must not wipe the list
	// or the default.
	_, _, err = h.registerNode(ctx, protocol.RegisterRequest{
		Name:      name,
		Token:     token,
		MachineID: machineID,
	})
	if err != nil {
		t.Fatalf("legacy reconnect: %v", err)
	}
	stored, err = st.Queries().GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node after legacy reconnect: %v", err)
	}
	if stored.DefaultAdapter != "codex" {
		t.Fatalf("legacy reconnect wiped default_adapter: %q", stored.DefaultAdapter)
	}
	if !slices.Equal(stored.AgentTypes, []string{"codex"}) {
		t.Fatalf("legacy reconnect wiped agent_types: %v", stored.AgentTypes)
	}
	if stored.Version != "v1.3.0" {
		t.Fatalf("legacy reconnect wiped version: %q", stored.Version)
	}
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
// non-terminal sessions and their tasks, broadcasting both changes.
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

	var sawSessionFailed, sawTaskFailed bool
	timeout := time.After(3 * time.Second)
	for !sawSessionFailed || !sawTaskFailed {
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
					tc.TaskID == task.ID && tc.To == protocol.TaskFailed {
					sawTaskFailed = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for cascade: sawSessionFailed=%v sawTaskFailed=%v", sawSessionFailed, sawTaskFailed)
		}
	}

	ctx := context.Background()
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionFailed {
		t.Fatalf("session status = %q (err %v), want failed", sess.Status, err)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil || got.Status != protocol.TaskFailed {
		t.Fatalf("task status = %q (err %v), want failed", got.Status, err)
	}
}

// A tool call is re-sent as its status changes; the fallback buffer must
// update that row instead of appending one copy per frame.
func TestAppendBufferedMessageUpdatesByID(t *testing.T) {
	h := &Hub{text: make(map[string]textBufferEntry)}
	ctx := context.Background()
	first := protocol.Message{
		ID: "tool-1", SessionID: "s1", Role: protocol.RoleTool, Content: "Read file",
		Tool: &protocol.ToolCall{ID: "call-1", Title: "Read file", Status: "pending"},
	}
	updated := first
	updated.Content = "Read file"
	updated.Tool = &protocol.ToolCall{ID: "call-1", Title: "Read file", Status: "completed"}

	h.appendBufferedMessage(ctx, "s1", first)
	h.appendBufferedMessage(ctx, "s1", updated)

	buf := h.textBuffer(ctx, "s1")
	if len(buf.Messages) != 1 {
		t.Fatalf("buffered messages = %d, want one updated row: %+v", len(buf.Messages), buf.Messages)
	}
	if buf.Messages[0].Tool == nil || buf.Messages[0].Tool.Status != "completed" {
		t.Fatalf("buffered tool = %+v, want completed", buf.Messages[0].Tool)
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

// TestEvSessionUsagePersistsCacheTokens pins the cache half of the usage
// pipeline: a report carrying cached read/write counters lands as nullable DB
// columns and the stats endpoint sums them with the matching prompt subtotal
// and report count (the cache-ratio denominator).
func TestEvSessionUsagePersistsCacheTokens(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("cache-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	read, write := int64(40), int64(10)
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: protocol.EvSessionUsage,
		Data: protocol.SessionUsage{
			SessionID:         task.SessionID,
			Model:             "claude-x",
			PromptTokens:      50,
			CompletionTokens:  80,
			TotalTokens:       200,
			CachedReadTokens:  &read,
			CachedWriteTokens: &write,
		},
	})); err != nil {
		t.Fatalf("send usage event: %v", err)
	}

	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := st.Queries().ListTokenUsageBySession(ctx, task.SessionID)
		if err != nil {
			t.Fatalf("list token usage: %v", err)
		}
		if len(rows) == 1 {
			row := rows[0]
			if row.CachedReadTokens == nil || *row.CachedReadTokens != read ||
				row.CachedWriteTokens == nil || *row.CachedWriteTokens != write {
				t.Fatalf("cache columns = %v/%v, want %d/%d",
					row.CachedReadTokens, row.CachedWriteTokens, read, write)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage row was not persisted (%d rows)", len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/stats/tokens?session_id="+task.SessionID)
	stats := decodeOK[protocol.TokenUsageStats](t, "GET /api/stats/tokens", resp, raw)
	wantTotal := protocol.TokenUsageTotals{
		PromptTokens: 50, CompletionTokens: 80, TotalTokens: 200,
		CachedReadTokens: 40, CachedWriteTokens: 10, CachePromptTokens: 50, CacheReports: 1,
	}
	if stats.Total != wantTotal {
		t.Fatalf("total = %+v, want %+v", stats.Total, wantTotal)
	}
	if len(stats.ByModel) != 1 {
		t.Fatalf("by_model = %+v, want one row", stats.ByModel)
	}
	wantRow := protocol.TokenUsageByModel{
		Model: "claude-x", PromptTokens: 50, CompletionTokens: 80, TotalTokens: 200,
		CachedReadTokens: 40, CachedWriteTokens: 10, CachePromptTokens: 50, CacheReports: 1, Turns: 1,
	}
	if stats.ByModel[0] != wantRow {
		t.Fatalf("by_model row = %+v, want %+v", stats.ByModel[0], wantRow)
	}
}

// TestEvSessionDeltaSurvivesReload pins the reload fix: streaming assistant
// chunks accumulate in the hub's ephemeral buffer and are exposed as the
// snapshot's partial_output until a complete assistant message supersedes
// them. Without this, refreshing mid-turn dropped everything streamed so far.
func TestEvSessionDeltaSurvivesReload(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("delta-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	for _, chunk := range []string{"Hel", "lo, ", "world"} {
		if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
			Type: protocol.EvSessionDelta,
			Data: protocol.SessionDelta{SessionID: task.SessionID, Text: chunk},
		})); err != nil {
			t.Fatalf("send delta: %v", err)
		}
	}

	snapshot := func() sessionSnapshot {
		t.Helper()
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID)
		return decodeOK[sessionSnapshot](t, "GET /api/sessions/{id}", resp, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := snapshot().PartialOutput; got == "Hello, world" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("partial_output = %q, want the accumulated stream", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A complete assistant message supersedes the partial.
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: task.SessionID,
		Message: protocol.Message{
			SessionID: task.SessionID,
			Role:      protocol.RoleAssistant,
			Content:   "Hello, world",
			CreatedAt: time.Now().UTC(),
		},
	})); err != nil {
		t.Fatalf("send session message: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		snap := snapshot()
		if snap.PartialOutput == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("partial_output = %q, want cleared by the final message", snap.PartialOutput)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEvSessionThoughtSurvivesReload pins reasoning restoration: thought
// frames are live-only in the wire contract, but the hub keeps a bounded copy
// in the ephemeral buffer so reopening the page mid-turn shows what the agent
// already reasoned. A complete message (or terminal state) clears it.
func TestEvSessionThoughtSurvivesReload(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("thought-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	for _, chunk := range []string{"weighing ", "the ", "options"} {
		if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
			Type: protocol.EvSessionThought,
			Data: protocol.SessionThought{SessionID: task.SessionID, Text: chunk},
		})); err != nil {
			t.Fatalf("send thought: %v", err)
		}
	}

	snapshot := func() sessionSnapshot {
		t.Helper()
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID)
		return decodeOK[sessionSnapshot](t, "GET /api/sessions/{id}", resp, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := snapshot().PartialThought; got == "weighing the options" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("partial_thought = %q, want the accumulated trace", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A complete assistant message supersedes the turn's reasoning.
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: task.SessionID,
		Message: protocol.Message{
			SessionID: task.SessionID,
			Role:      protocol.RoleAssistant,
			Content:   "done",
			CreatedAt: time.Now().UTC(),
		},
	})); err != nil {
		t.Fatalf("send session message: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if got := snapshot().PartialThought; got == "" {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("partial_thought = %q, want cleared by the final message", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postJSON[T any](t *testing.T, token, url string, body any) T {
	t.Helper()
	resp, raw := postRaw(t, token, url, body)
	return decodeOK[T](t, url, resp, raw)
}

// A node reports the agent-side session id once the adapter opens (or
// reattaches) a session. The hub must persist it: without it a finished
// session cannot be replayed and GET /api/sessions/{id}/messages answers 503.
func TestNodeSessionStartedRecordsExternalID(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-it-ext"))
	task := createWorkspaceAndTask(t, auth.token, ts, node.ID)

	ctx := context.Background()
	session, err := st.Queries().CreateSession(ctx, store.CreateSessionParams{
		TaskID:    task.ID,
		NodeID:    node.ID,
		AgentType: "fake",
		Status:    protocol.SessionRunning,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	h.handleNodeMessage(ctx, node.ID, protocol.NewEnvelope(protocol.MsgSessionStarted, protocol.SessionStarted{
		SessionID:         session.ID,
		ExternalSessionID: "agent-side-42",
	}))

	got, err := st.Queries().GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.ExternalSessionID != "agent-side-42" {
		t.Fatalf("external_session_id = %q, want agent-side-42", got.ExternalSessionID)
	}
}

// A reconnect supersedes the previous connection. The stale handler must not
// evict the live connection or mark the node offline: doing so stops its pings
// and kills the node again one read-timeout later, which flaps forever.
func TestStaleConnectionCleanupKeepsReconnectedNode(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	name := uniqueName("hub-it-recon")

	// First registration burns a single-use pairing token and yields the node
	// token every later handshake must present.
	join := postJSON[struct {
		Token string `json:"token"`
	}](t, auth.token, ts.URL+"/api/nodes/join-tokens", map[string]int{})
	first, firstResp := dialNodeAs(t, ts, st, name, "", join.Token)
	if firstResp.Token == "" {
		t.Fatal("hub did not issue a node token")
	}
	second, secondResp := dialNodeAs(t, ts, st, name, firstResp.Token, "")
	defer func() { _ = second.Close() }()
	nodeID := firstResp.NodeID
	if secondResp.NodeID != nodeID {
		t.Fatalf("reconnect created a new node %q, want %q", secondResp.NodeID, nodeID)
	}

	// The hub closes the superseded socket as soon as the new one registers;
	// its handler then runs its cleanup. The node must stay online.
	_ = first.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.nodeOnline(nodeID) {
			t.Fatal("stale connection cleanup evicted the reconnected node")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A node that reconnects within the grace period keeps its running session:
// the offline cascade must not fail a task for a one-second blip, because the
// adapter process on the node is still alive.
func TestReconnectWithinGraceKeepsSessionRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	// Long enough that the reconnect below always lands inside the window.
	h.offlineGrace = 2 * time.Second
	name := uniqueName("hub-it-grace")

	join := postJSON[struct {
		Token string `json:"token"`
	}](t, auth.token, ts.URL+"/api/nodes/join-tokens", map[string]int{})
	c, reg := dialNodeAs(t, ts, st, name, "", join.Token)
	if reg.NodeID == "" {
		t.Fatalf("register: %s", reg.Error)
	}
	task := createWorkspaceAndTask(t, auth.token, ts, reg.NodeID)
	if task.SessionID == "" {
		t.Fatal("dispatch created no session")
	}

	// Drop the connection and reconnect right away, exactly like the daemon:
	// the adapters survived in-process, so the handshake lists the session.
	_ = c.Close()
	back, backReg := dialNodeFull(t, ts, st, name, reg.Token, "", []string{task.SessionID})
	defer func() { _ = back.Close() }()
	if backReg.NodeID != reg.NodeID {
		t.Fatalf("reconnect node id = %q, want %q", backReg.NodeID, reg.NodeID)
	}

	// Past the grace period the node is still connected, so nothing was failed.
	time.Sleep(3 * time.Second)
	session, err := st.Queries().GetSession(context.Background(), task.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q, want running", session.Status)
	}
}

// An upgraded or crashed node restarts and comes back inside the offline grace
// window with no live adapters. The orphaned running session is failed, and the
// task is re-dispatched once with its original prompt (the documented default
// retry budget); a session parked on a user question is not replayed.
func TestRestartedNodeRetriesOrphanedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	// The reconnect below lands well inside the window; without the handshake
	// list this is exactly the state that used to stick.
	h.offlineGrace = 30 * time.Second
	name := uniqueName("hub-it-restart")

	join := postJSON[struct {
		Token string `json:"token"`
	}](t, auth.token, ts.URL+"/api/nodes/join-tokens", map[string]int{})
	c, reg := dialNodeFull(t, ts, st, name, "", join.Token, []string{})
	if reg.NodeID == "" {
		t.Fatalf("register: %s", reg.Error)
	}
	task := createWorkspaceAndTask(t, auth.token, ts, reg.NodeID)
	if task.SessionID == "" {
		t.Fatal("dispatch created no session")
	}
	prompt := task.Prompt

	// Restart: a fresh process, so no adapter survived the old session.
	_ = c.Close()
	back, backReg := dialNodeFull(t, ts, st, name, reg.Token, "", []string{})
	defer func() { _ = back.Close() }()
	if backReg.NodeID != reg.NodeID {
		t.Fatalf("reconnect node id = %q, want %q", backReg.NodeID, reg.NodeID)
	}

	// The retry is a new session carrying the original prompt.
	env := waitEnvelopeFrom(t, back, protocol.MsgTaskStart)
	var cmd protocol.TaskStartCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode task.start: %v", err)
	}
	if cmd.Prompt != prompt {
		t.Fatalf("retry prompt = %q, want %q", cmd.Prompt, prompt)
	}
	if cmd.SessionID == task.SessionID {
		t.Fatalf("retry reused session %q; want a fresh session", cmd.SessionID)
	}

	ctx := context.Background()
	oldSession, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || oldSession.Status != protocol.SessionFailed {
		t.Fatalf("old session status = %q (err %v), want failed", oldSession.Status, err)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != protocol.TaskRunning || got.SessionID != cmd.SessionID {
		t.Fatalf("task after retry = %q session %q, want running/%q",
			got.Status, got.SessionID, cmd.SessionID)
	}
	var retries int
	if err := st.Pool().QueryRow(ctx, "SELECT retry_count FROM task WHERE id = $1", task.ID).Scan(&retries); err != nil {
		t.Fatalf("read retry_count: %v", err)
	}
	if retries != 1 {
		t.Fatalf("retry_count = %d, want 1", retries)
	}

	// The default budget is one retry: a second restart of the retried session
	// fails the task instead of replaying it again.
	retriedSession := got.SessionID
	_ = back.Close()
	back2, back2Reg := dialNodeFull(t, ts, st, name, reg.Token, "", []string{})
	defer func() { _ = back2.Close() }()
	if back2Reg.NodeID != reg.NodeID {
		t.Fatalf("second reconnect node id = %q, want %q", back2Reg.NodeID, reg.NodeID)
	}
	_ = back2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var extra protocol.Envelope
	if err := back2.ReadJSON(&extra); err == nil && extra.Type == protocol.MsgTaskStart {
		t.Fatal("retry budget exhausted, but the task was dispatched again")
	}
	_ = back2.SetReadDeadline(time.Time{})

	deadline := time.Now().Add(5 * time.Second)
	for {
		session, serr := st.Queries().GetSession(ctx, retriedSession)
		current, terr := st.Queries().GetTask(ctx, task.ID)
		if serr == nil && terr == nil &&
			session.Status == protocol.SessionFailed && current.Status == protocol.TaskFailed {
			if current.SessionID != retriedSession {
				t.Fatalf("failed task points at session %q, want %q", current.SessionID, retriedSession)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("second restart = session %q task %q (errs %v/%v), want failed/failed",
				session.Status, current.Status, serr, terr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A session waiting for a user answer is not replayed from scratch on restart:
// the operator's pending question would be lost. It fails like before and the
// operator continues it with a reply.
func TestRestartedNodeDoesNotRetryAwaitingUserSession(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	h.offlineGrace = 30 * time.Second
	name := uniqueName("hub-it-awaiting")

	join := postJSON[struct {
		Token string `json:"token"`
	}](t, auth.token, ts.URL+"/api/nodes/join-tokens", map[string]int{})
	c, reg := dialNodeFull(t, ts, st, name, "", join.Token, []string{})
	if reg.NodeID == "" {
		t.Fatalf("register: %s", reg.Error)
	}
	task := createWorkspaceAndTask(t, auth.token, ts, reg.NodeID)
	if task.SessionID == "" {
		t.Fatal("dispatch created no session")
	}

	// The agent parks on a question (the hub derives awaiting_user for the task).
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: protocol.EvSessionStateChanged,
		Data: protocol.SessionStateChanged{SessionID: task.SessionID, Status: protocol.SessionAwaitingUser},
		Ts:   time.Now().UTC(),
	})); err != nil {
		t.Fatalf("write awaiting_user event: %v", err)
	}
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil && got.Status == protocol.TaskAwaitingUser {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach awaiting_user (err %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	_ = c.Close()
	back, backReg := dialNodeFull(t, ts, st, name, reg.Token, "", []string{})
	defer func() { _ = back.Close() }()
	if backReg.NodeID != reg.NodeID {
		t.Fatalf("reconnect node id = %q, want %q", backReg.NodeID, reg.NodeID)
	}
	// Nothing is re-dispatched.
	_ = back.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var env protocol.Envelope
	if err := back.ReadJSON(&env); err == nil && env.Type == protocol.MsgTaskStart {
		t.Fatal("awaiting_user session was retried on node restart")
	}
	_ = back.SetReadDeadline(time.Time{})

	// The reconcile runs after the handshake response; poll for its effect.
	ctx = context.Background()
	deadline = time.Now().Add(5 * time.Second)
	for {
		session, serr := st.Queries().GetSession(ctx, task.SessionID)
		got, terr := st.Queries().GetTask(ctx, task.ID)
		if serr == nil && terr == nil &&
			session.Status == protocol.SessionFailed && got.Status == protocol.TaskFailed {
			if got.SessionID != task.SessionID {
				t.Fatalf("failed task points at session %q, want the original %q",
					got.SessionID, task.SessionID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("awaiting_user restart = session %q task %q (errs %v/%v), want failed/failed",
				session.Status, got.Status, serr, terr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The offline cascade fails a session after the grace period, but a node that
// only lost its link keeps its adapter alive. A reconnect that lists that
// session must drop the scheduled retry instead of starting a second run.
func TestReconnectWithLiveAdapterCancelsScheduledRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	name := uniqueName("hub-it-live-retry")
	join := postJSON[struct {
		Token string `json:"token"`
	}](t, auth.token, ts.URL+"/api/nodes/join-tokens", map[string]int{})
	c, reg := dialNodeFull(t, ts, st, name, "", join.Token, []string{})
	if reg.NodeID == "" {
		t.Fatalf("register: %s", reg.Error)
	}
	task := createWorkspaceAndTask(t, auth.token, ts, reg.NodeID)
	if task.SessionID == "" {
		t.Fatal("dispatch created no session")
	}

	// The link drops; the 50ms test grace expires and the cascade marks the
	// task failed with a retry pending.
	_ = c.Close()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var pending bool
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil && got.Status == protocol.TaskFailed {
			if err := st.Pool().QueryRow(ctx,
				"SELECT retry_pending FROM task WHERE id = $1", task.ID).Scan(&pending); err != nil {
				t.Fatalf("read retry_pending: %v", err)
			}
			if pending {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not schedule a retry (err %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The same daemon process reconnects: its adapter survived the outage.
	back, backReg := dialNodeFull(t, ts, st, name, reg.Token, "", []string{task.SessionID})
	defer func() { _ = back.Close() }()
	if backReg.NodeID != reg.NodeID {
		t.Fatalf("reconnect node id = %q, want %q", backReg.NodeID, reg.NodeID)
	}
	_ = back.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var env protocol.Envelope
	if err := back.ReadJSON(&env); err == nil && env.Type == protocol.MsgTaskStart {
		t.Fatal("retry dispatched although the original adapter reconnected")
	}
	_ = back.SetReadDeadline(time.Time{})

	var pending bool
	if err := st.Pool().QueryRow(ctx,
		"SELECT retry_pending FROM task WHERE id = $1", task.ID).Scan(&pending); err != nil {
		t.Fatalf("read retry_pending: %v", err)
	}
	if pending {
		t.Fatal("retry_pending survived a reconnect with the live adapter")
	}
	var retries int
	if err := st.Pool().QueryRow(ctx,
		"SELECT retry_count FROM task WHERE id = $1", task.ID).Scan(&retries); err != nil {
		t.Fatalf("read retry_count: %v", err)
	}
	if retries != 0 {
		t.Fatalf("retry_count = %d after a cancelled retry, want 0", retries)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil || got.SessionID != task.SessionID {
		t.Fatalf("task = %q session %q (err %v), want the original session %q",
			got.Status, got.SessionID, err, task.SessionID)
	}
}

// waitEnvelopeFrom reads from a scripted node until the wanted command type
// arrives, failing on timeout. Unlike waitEnvelope it owns the socket read, so
// it cannot be combined with another reader.
func waitEnvelopeFrom(t *testing.T, c *websocket.Conn, typ string) protocol.Envelope {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	for {
		var env protocol.Envelope
		if err := c.ReadJSON(&env); err != nil {
			t.Fatalf("read node command: %v", err)
		}
		if env.Type == typ {
			return env
		}
	}
}

// A task without an instruction would open an agent session that never gets a
// prompt and sits in "running" forever, so both creation and start reject it.
func TestTaskPromptIsRequired(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-it-noprompt"))
	workspace := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": node.ID,
		"name":    uniqueName("it-ws"),
		"path":    "/tmp/it-ws",
	})

	resp, raw := postRaw(t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": workspace.ID,
		"title":        "no instruction",
		"agent_type":   "fake",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create without prompt = %d %s, want 400", resp.StatusCode, raw)
	}

	// A backlog task that somehow lost its prompt cannot be started either.
	task := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]any{
		"workspace_id": workspace.ID,
		"title":        "backlog without instruction",
		"prompt":       "placeholder",
		"agent_type":   "fake",
		"auto_start":   false,
	})
	if _, err := st.Pool().Exec(context.Background(), "UPDATE task SET prompt = '' WHERE id = $1", task.ID); err != nil {
		t.Fatalf("clear prompt: %v", err)
	}
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID+"/start", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("start without prompt = %d %s, want 400", resp.StatusCode, raw)
	}
}

func TestCreateTaskDerivesTitleFromPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-it-title"))
	workspace := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": node.ID,
		"name":    uniqueName("it-ws"),
		"path":    "/tmp/it-ws",
	})

	task := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": workspace.ID,
		"prompt":       "  Update the flake inputs\nand rebuild  ",
		"agent_type":   "fake",
	})
	if task.Title != "Update the flake inputs" {
		t.Fatalf("title = %q, want the first prompt line", task.Title)
	}
}

// A live session's text buffer starts empty; the endpoint must answer [] (not
// null) so clients can treat the payload as an array. The web form updates that
// cache optimistically before sending, and a null crashed the send.
func TestListMessagesEmptyBufferIsArray(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-it-empty"))
	task := createWorkspaceAndTask(t, auth.token, ts, node.ID)
	session, err := st.Queries().CreateSession(context.Background(), store.CreateSessionParams{
		TaskID:    task.ID,
		NodeID:    node.ID,
		AgentType: "fake",
		Status:    protocol.SessionRunning,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+session.ID+"/messages")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages = %d %s", resp.StatusCode, raw)
	}
	if got := strings.TrimSpace(string(raw)); got != "[]" {
		t.Fatalf("empty buffer = %s, want []", got)
	}
}

// The instruction is the first transcript line: a live session must not open
// empty while the agent works through its first tool calls.
func TestDispatchRecordsPromptInTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-it-prompt"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatal("dispatch created no session")
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/messages")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages = %d %s", resp.StatusCode, raw)
	}
	var msgs []protocol.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != protocol.RoleUser || msgs[0].Content != "do the thing" {
		t.Fatalf("transcript = %+v, want the task prompt as the first user message", msgs)
	}
}

// TestSessionTitleBackfillsProvisionalTask pins the title policy: a task
// created without a title starts with the prompt's first line and is renamed
// when the agent reports its session title; an operator-supplied title is
// never overwritten.
func TestSessionTitleBackfillsProvisionalTask(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("title-node"))
	defer func() { _ = c.Close() }()

	// The session.title frames below travel on this same connection, so the
	// reader must run concurrently with the test.
	starts := make(chan protocol.TaskStartCommand, 4)
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
			if env.Decode(&cmd) == nil {
				select {
				case starts <- cmd:
				default:
				}
			}
		}
	}()

	workspace := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID,
		"name":    uniqueName("title-ws"),
		"path":    "/tmp/title-ws",
	})
	auto := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": workspace.ID,
		"prompt":       "Fix the parser\nAdd regression tests.",
		"agent_type":   "fake",
	})
	if auto.Title != "Fix the parser" {
		t.Fatalf("provisional title = %q, want the first prompt line", auto.Title)
	}
	if auto.SessionID == "" {
		t.Fatalf("provisional task was not dispatched: %+v", auto)
	}

	supplied := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": workspace.ID,
		"title":        "Operator title",
		"prompt":       "Do the thing",
		"agent_type":   "fake",
	})
	if supplied.SessionID == "" {
		t.Fatalf("titled task was not dispatched: %+v", supplied)
	}

	// The hub only asks the agent for a name while the task title is
	// provisional; an operator title must not trigger the request.
	for _, want := range []struct {
		title string
		auto  bool
	}{{"Fix the parser", true}, {"Operator title", false}} {
		select {
		case cmd := <-starts:
			if cmd.AutoTitle != want.auto {
				t.Fatalf("task.start for %q: auto_title = %v, want %v", want.title, cmd.AutoTitle, want.auto)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no task.start delivered for %q", want.title)
		}
	}

	events, unsubscribe := h.bus.Subscribe(EventScope{All: true})
	defer unsubscribe()

	ctx := context.Background()
	h.handleNodeMessage(ctx, nodeID, protocol.NewEnvelope(protocol.MsgSessionTitle, protocol.SessionTitle{
		SessionID: auto.SessionID,
		Title:     "Parser recovery",
	}))

	stored, err := st.Queries().GetTask(ctx, auto.ID)
	if err != nil {
		t.Fatalf("get provisional task: %v", err)
	}
	if stored.Title != "Parser recovery" {
		t.Fatalf("provisional title = %q, want the agent title", stored.Title)
	}

	select {
	case evt := <-events:
		d, ok := evt.Data.(protocol.TaskTitleChanged)
		if evt.Type != protocol.EvTaskTitleChanged || !ok || d.TaskID != auto.ID || d.Title != "Parser recovery" {
			t.Fatalf("event = %q %+v, want task.title_changed for %s", evt.Type, evt.Data, auto.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task.title_changed")
	}

	h.handleNodeMessage(ctx, nodeID, protocol.NewEnvelope(protocol.MsgSessionTitle, protocol.SessionTitle{
		SessionID: supplied.SessionID,
		Title:     "Agent title",
	}))
	stored, err = st.Queries().GetTask(ctx, supplied.ID)
	if err != nil {
		t.Fatalf("get titled task: %v", err)
	}
	if stored.Title != "Operator title" {
		t.Fatalf("operator title = %q, want it preserved", stored.Title)
	}
}
