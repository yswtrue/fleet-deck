package hub

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/adapters/fake"
	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// wireSink mirrors internal/node.connSink's EventSink -> wire mapping (hub
// tests must not import internal/node): every adapter callback is written to
// the scripted node's WebSocket exactly as a real node daemon would report it.
type wireSink struct {
	t        *testing.T
	c        *websocket.Conn
	mu       sync.Mutex
	done     chan struct{}
	doneOnce sync.Once
}

func newWireSink(t *testing.T, c *websocket.Conn) *wireSink {
	return &wireSink{t: t, c: c, done: make(chan struct{})}
}

func (s *wireSink) write(env protocol.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.c.WriteJSON(env); err != nil {
		s.t.Errorf("wire sink write: %v", err)
	}
}

func (s *wireSink) event(typ string, payload any) {
	s.write(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: typ, Data: payload, Ts: time.Now().UTC(),
	}))
}

func (s *wireSink) TextDelta(_ context.Context, sessionID, text string) {
	s.event(protocol.EvSessionDelta, protocol.SessionDelta{SessionID: sessionID, Text: text})
}

func (s *wireSink) Message(_ context.Context, sessionID string, msg protocol.Message) {
	s.write(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
		SessionID: sessionID, Message: msg,
	}))
}

func (s *wireSink) TodoUpdate(_ context.Context, sessionID string, todos []protocol.Todo) {
	s.write(protocol.NewEnvelope(protocol.MsgTodoUpdate, protocol.TodoUpdate{
		SessionID: sessionID, Todos: todos,
	}))
}

func (s *wireSink) StateChange(_ context.Context, sessionID string, status protocol.SessionStatus) {
	s.event(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: status})
}

func (s *wireSink) UserInputRequested(_ context.Context, sessionID, prompt string) {
	s.event(protocol.EvSessionUserInputRequest, protocol.SessionUserInputRequested{SessionID: sessionID, Prompt: prompt})
}

func (s *wireSink) Usage(_ context.Context, sessionID string, u adapters.Usage) {
	s.event(protocol.EvSessionUsage, protocol.SessionUsage{
		SessionID:        sessionID,
		Model:            u.Model,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	})
}

func (s *wireSink) Done(_ context.Context, sessionID string) {
	s.event(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: protocol.SessionDone})
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *wireSink) Error(_ context.Context, sessionID string, _ error) {
	s.event(protocol.EvSessionStateChanged, protocol.SessionStateChanged{SessionID: sessionID, Status: protocol.SessionFailed})
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *wireSink) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the adapter to finish")
	}
}

// ---- EventSink -> hub persistence -----------------------------------------

// TestEvSessionUsagePersistsTokenUsageRow drives the real fake adapter over a
// scripted node's WebSocket and pins the full chain: the adapter's usage
// report travels as MsgEvent/EvSessionUsage and lands as a token_usage row
// carrying the session, its task, and the counters verbatim.
func TestEvSessionUsagePersistsTokenUsageRow(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("usage-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatal("dispatched task has no session_id")
	}

	sink := newWireSink(t, c)
	a := fake.New()
	a.TickInterval = time.Millisecond
	if _, err := a.Start(context.Background(), adapters.StartRequest{
		SessionID: task.SessionID,
		TaskID:    task.ID,
		AgentType: "fake",
		Prompt:    "do the thing",
	}, sink); err != nil {
		t.Fatalf("fake adapter Start: %v", err)
	}
	sink.waitDone(t)

	// The hub persists reports as they arrive; poll until the row lands.
	var rows []store.TokenUsage
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		rows, err = st.Queries().ListTokenUsageBySession(context.Background(), task.SessionID)
		if err != nil {
			t.Fatalf("list token usage: %v", err)
		}
		if len(rows) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("token_usage rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.SessionID != task.SessionID || row.TaskID != task.ID {
		t.Errorf("row ids = (%s, %s), want (%s, %s)", row.SessionID, row.TaskID, task.SessionID, task.ID)
	}
	want := store.TokenUsage{Model: "fake", PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200}
	if row.Model != want.Model || row.PromptTokens != want.PromptTokens ||
		row.CompletionTokens != want.CompletionTokens || row.TotalTokens != want.TotalTokens {
		t.Errorf("row usage = %s %d/%d/%d, want %s %d/%d/%d",
			row.Model, row.PromptTokens, row.CompletionTokens, row.TotalTokens,
			want.Model, want.PromptTokens, want.CompletionTokens, want.TotalTokens)
	}

	// The stats endpoint counts the same row.
	resp, raw := getRaw(t, auth.token, ts.URL+"/api/stats/tokens?session_id="+task.SessionID)
	stats := decodeOK[protocol.TokenUsageStats](t, "GET /api/stats/tokens", resp, raw)
	if stats.Total != (protocol.TokenUsageTotals{PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200}) {
		t.Errorf("total = %+v, want 120/80/200", stats.Total)
	}
	wantModel := []protocol.TokenUsageByModel{{Model: "fake", PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200, Turns: 1}}
	if len(stats.ByModel) != 1 || stats.ByModel[0] != wantModel[0] {
		t.Errorf("by_model = %+v, want %+v", stats.ByModel, wantModel)
	}
}

// ---- aggregation -----------------------------------------------------------

// seedUsage writes one usage row directly through the store (the write path
// itself is pinned by TestEvSessionUsagePersistsTokenUsageRow).
func seedUsage(t *testing.T, st *store.Store, sessionID, taskID, model string, prompt, completion, total int64) {
	t.Helper()
	if _, err := st.Queries().InsertTokenUsage(context.Background(), store.InsertTokenUsageParams{
		SessionID:        sessionID,
		TaskID:           taskID,
		Model:            model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
	}); err != nil {
		t.Fatalf("insert token usage: %v", err)
	}
}

// TestStatsTokensAggregation pins GET /api/stats/tokens: cross-model totals,
// the per-model breakdown sorted by total descending with turn counts, and
// every filter (session, workspace, node, from/to window).
func TestStatsTokensAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, node1 := dialNode(t, ts, st, uniqueName("stats-node1"))
	defer func() { _ = c1.Close() }()
	c2, node2 := dialNode(t, ts, st, uniqueName("stats-node2"))
	defer func() { _ = c2.Close() }()

	task1 := createWorkspaceAndTask(t, auth.token, ts, node1)
	task2 := createWorkspaceAndTask(t, auth.token, ts, node2)

	seedUsage(t, st, task1.SessionID, task1.ID, "alpha", 10, 5, 15)
	seedUsage(t, st, task1.SessionID, task1.ID, "alpha", 5, 5, 10)
	seedUsage(t, st, task1.SessionID, task1.ID, "beta", 1, 1, 2)
	seedUsage(t, st, task2.SessionID, task2.ID, "alpha", 100, 100, 200)

	statsURL := ts.URL + "/api/stats/tokens"
	full := protocol.TokenUsageStats{
		Total: protocol.TokenUsageTotals{PromptTokens: 116, CompletionTokens: 111, TotalTokens: 227},
		ByModel: []protocol.TokenUsageByModel{
			{Model: "alpha", PromptTokens: 115, CompletionTokens: 110, TotalTokens: 225, Turns: 3},
			{Model: "beta", PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2, Turns: 1},
		},
	}
	task1Only := protocol.TokenUsageStats{
		Total: protocol.TokenUsageTotals{PromptTokens: 16, CompletionTokens: 11, TotalTokens: 27},
		ByModel: []protocol.TokenUsageByModel{
			{Model: "alpha", PromptTokens: 15, CompletionTokens: 10, TotalTokens: 25, Turns: 2},
			{Model: "beta", PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2, Turns: 1},
		},
	}
	task2Only := protocol.TokenUsageStats{
		Total: protocol.TokenUsageTotals{PromptTokens: 100, CompletionTokens: 100, TotalTokens: 200},
		ByModel: []protocol.TokenUsageByModel{
			{Model: "alpha", PromptTokens: 100, CompletionTokens: 100, TotalTokens: 200, Turns: 1},
		},
	}
	empty := protocol.TokenUsageStats{
		Total:   protocol.TokenUsageTotals{},
		ByModel: []protocol.TokenUsageByModel{},
	}

	now := time.Now()
	from := urlTime(now.Add(-time.Hour))
	to := urlTime(now.Add(time.Hour))
	futureFrom := urlTime(now.Add(time.Hour))
	pastTo := urlTime(now.Add(-time.Hour))

	cases := []struct {
		query string
		want  protocol.TokenUsageStats
	}{
		{"", full},
		{"?session_id=" + task1.SessionID, task1Only},
		{"?session_id=" + task2.SessionID, task2Only},
		{"?workspace_id=" + task1.WorkspaceID, task1Only},
		{"?node_id=" + node2, task2Only},
		{"?from=" + from + "&to=" + to, full},
		{"?from=" + futureFrom, empty},
		{"?to=" + pastTo, empty},
	}
	for _, tc := range cases {
		resp, raw := getRaw(t, auth.token, statsURL+tc.query)
		got := decodeOK[protocol.TokenUsageStats](t, "GET /api/stats/tokens"+tc.query, resp, raw)
		if got.Total != tc.want.Total {
			t.Errorf("%q total = %+v, want %+v", tc.query, got.Total, tc.want.Total)
		}
		if len(got.ByModel) != len(tc.want.ByModel) {
			t.Fatalf("%q by_model = %+v, want %+v", tc.query, got.ByModel, tc.want.ByModel)
		}
		for i := range tc.want.ByModel {
			if got.ByModel[i] != tc.want.ByModel[i] {
				t.Errorf("%q by_model[%d] = %+v, want %+v", tc.query, i, got.ByModel[i], tc.want.ByModel[i])
			}
		}
	}

	// An explicit filter that resolves to nothing visible is a 404; a malformed
	// time bound is a 400.
	resp, raw := getRaw(t, auth.token, statsURL+"?session_id="+randomUUID(t))
	requireStatus(t, "unknown session filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, auth.token, statsURL+"?workspace_id="+randomUUID(t))
	requireStatus(t, "unknown workspace filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, auth.token, statsURL+"?node_id="+randomUUID(t))
	requireStatus(t, "unknown node filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, auth.token, statsURL+"?from=yesterday")
	requireStatus(t, "malformed from", resp, raw, http.StatusBadRequest)
	resp, raw = getRaw(t, auth.token, statsURL+"?to=tomorrow")
	requireStatus(t, "malformed to", resp, raw, http.StatusBadRequest)
}

// TestStatsTokensVisibility pins the binding scoping of the aggregate: a
// non-admin only counts rows whose task lives under a bound node; explicit
// filters naming invisible ids are 404 (no existence leak); unbinding empties
// the view.
func TestStatsTokensVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, nodeA := dialNode(t, ts, st, uniqueName("vis-stats-a"))
	defer func() { _ = c1.Close() }()
	c2, nodeB := dialNode(t, ts, st, uniqueName("vis-stats-b"))
	defer func() { _ = c2.Close() }()

	taskA := createWorkspaceAndTask(t, auth.token, ts, nodeA)
	taskB := createWorkspaceAndTask(t, auth.token, ts, nodeB)
	seedUsage(t, st, taskA.SessionID, taskA.ID, "alpha", 10, 5, 15)
	seedUsage(t, st, taskB.SessionID, taskB.ID, "alpha", 100, 100, 200)

	user, userToken := setupUserWithRole(t, st, "user")
	bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeA)

	statsURL := ts.URL + "/api/stats/tokens"
	subtreeOnly := protocol.TokenUsageTotals{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	// Unfiltered: only the bound subtree counts.
	resp, raw := getRaw(t, userToken, statsURL)
	stats := decodeOK[protocol.TokenUsageStats](t, "user stats", resp, raw)
	if stats.Total != subtreeOnly {
		t.Errorf("unfiltered total = %+v, want %+v", stats.Total, subtreeOnly)
	}

	// Explicit filters naming invisible ids are 404 even though they exist.
	resp, raw = getRaw(t, userToken, statsURL+"?node_id="+nodeB)
	requireStatus(t, "unbound node filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, userToken, statsURL+"?workspace_id="+taskB.WorkspaceID)
	requireStatus(t, "unbound workspace filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, userToken, statsURL+"?session_id="+taskB.SessionID)
	requireStatus(t, "unbound session filter", resp, raw, http.StatusNotFound)

	// Explicit filters on the bound subtree resolve and still stay scoped.
	resp, raw = getRaw(t, userToken, statsURL+"?node_id="+nodeA)
	stats = decodeOK[protocol.TokenUsageStats](t, "bound node filter", resp, raw)
	if stats.Total != subtreeOnly {
		t.Errorf("bound node total = %+v, want %+v", stats.Total, subtreeOnly)
	}
	resp, raw = getRaw(t, userToken, statsURL+"?session_id="+taskA.SessionID)
	stats = decodeOK[protocol.TokenUsageStats](t, "bound session filter", resp, raw)
	if stats.Total != subtreeOnly {
		t.Errorf("bound session total = %+v, want %+v", stats.Total, subtreeOnly)
	}

	// Unbinding empties the view and flips reads back to 404.
	bindUserToNodes(t, ts.URL, auth.token, user.ID)
	resp, raw = getRaw(t, userToken, statsURL)
	stats = decodeOK[protocol.TokenUsageStats](t, "unbound stats", resp, raw)
	if stats.Total != (protocol.TokenUsageTotals{}) || len(stats.ByModel) != 0 {
		t.Errorf("unbound total = %+v by_model = %+v, want zeros and empty", stats.Total, stats.ByModel)
	}
	resp, raw = getRaw(t, userToken, statsURL+"?session_id="+taskA.SessionID)
	requireStatus(t, "session filter after unbind", resp, raw, http.StatusNotFound)
}

// urlTime formats t as RFC3339 for a query string.
func urlTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
