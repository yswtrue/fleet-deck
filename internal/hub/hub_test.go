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
		Name:         name,
		Host:         "test-host",
		Status:       protocol.NodeOffline,
		Capabilities: []string{"fake"},
		TokenHash:    "",
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

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}

	req := protocol.RegisterRequest{
		Name:         name,
		Host:         "test-host",
		Capabilities: []string{"fake"},
		Token:        token,
		MachineID:    "test-machine:" + name,
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
		Name:         name,
		Host:         "test-host",
		AgentTypes:   agentTypes,
		Capabilities: agentTypes,
		JoinToken:    testJoinToken,
		MachineID:    machineID,
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

	// A reconnect with a changed list replaces the stored one (a newly
	// installed or removed agent must show up without re-pairing).
	_, _, err = h.registerNode(ctx, protocol.RegisterRequest{
		Name:       name,
		Token:      token,
		AgentTypes: []string{"codex"},
		MachineID:  machineID,
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

	// A daemon that predates the field sends nil; that must not wipe the list.
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
	if !slices.Equal(stored.AgentTypes, []string{"codex"}) {
		t.Fatalf("legacy reconnect wiped agent_types: %v", stored.AgentTypes)
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

	// Drop the connection and reconnect right away, exactly like the daemon.
	_ = c.Close()
	back, backReg := dialNodeAs(t, ts, st, name, reg.Token, "")
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
