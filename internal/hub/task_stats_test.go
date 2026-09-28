package hub

import (
	"context"
	"net/http"
	"testing"
	"time"

	"fleetdeck/internal/protocol"
)

// wantTaskStats compares a GET /api/stats/tasks payload by totals plus a
// status map (the aggregate is order-insensitive once baselines are folded in).
func wantTaskStats(t *testing.T, label string, got, want protocol.TaskStats) {
	t.Helper()
	if got.Created != want.Created || got.Completed != want.Completed {
		t.Errorf("%s = created %d completed %d, want created %d completed %d",
			label, got.Created, got.Completed, want.Created, want.Completed)
	}
	gotByStatus := map[protocol.TaskStatus]int64{}
	for _, row := range got.ByStatus {
		gotByStatus[row.Status] += row.Count
	}
	wantByStatus := map[protocol.TaskStatus]int64{}
	for _, row := range want.ByStatus {
		wantByStatus[row.Status] += row.Count
	}
	if len(gotByStatus) != len(wantByStatus) {
		t.Fatalf("%s by_status = %v, want %v", label, gotByStatus, wantByStatus)
	}
	for status, count := range wantByStatus {
		if gotByStatus[status] != count {
			t.Errorf("%s by_status[%s] = %d, want %d", label, status, gotByStatus[status], count)
		}
	}
}

// mergeStats folds one aggregate into another (baseline + delta).
func mergeStats(a, b protocol.TaskStats) protocol.TaskStats {
	out := protocol.TaskStats{
		Created:   a.Created + b.Created,
		Completed: a.Completed + b.Completed,
		ByStatus:  append([]protocol.TaskStatsStatusCount(nil), a.ByStatus...),
	}
	for _, row := range b.ByStatus {
		out.ByStatus = append(out.ByStatus, row)
	}
	return out
}

// TestStatsTasksAggregation pins GET /api/stats/tasks: the created/completed
// counts, the by-status split of the window's tasks, every filter (workspace,
// node, from/to window) and the error contract (404 on invisible ids, 400 on
// malformed bounds). Unfiltered reads are asserted as baseline + delta: the
// task table is shared with the rest of the suite, so exact fleet-wide totals
// would couple this test to leftover rows.
func TestStatsTasksAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, node1 := dialNode(t, ts, st, uniqueName("taskstats-node1"))
	defer func() { _ = c1.Close() }()
	c2, node2 := dialNode(t, ts, st, uniqueName("taskstats-node2"))
	defer func() { _ = c2.Close() }()

	statsURL := ts.URL + "/api/stats/tasks"
	now := time.Now()
	from := urlTime(now.Add(-time.Hour))
	to := urlTime(now.Add(time.Hour))
	futureFrom := urlTime(now.Add(time.Hour))
	pastTo := urlTime(now.Add(-time.Hour))

	// Baselines captured before seeding: what the shared table already holds.
	get := func(label, query string) protocol.TaskStats {
		t.Helper()
		resp, raw := getRaw(t, auth.token, statsURL+query)
		return decodeOK[protocol.TaskStats](t, label, resp, raw)
	}
	base := get("baseline", "")
	baseWindow := get("baseline window", "?from="+from+"&to="+to)

	task1 := createWorkspaceAndTask(t, auth.token, ts, node1)
	task2 := createWorkspaceAndTask(t, auth.token, ts, node2)

	// Task1 finishes inside the window: completed counts it.
	if _, err := st.Queries().UpdateTaskStatus(context.Background(), task1.ID, protocol.TaskDone); err != nil {
		t.Fatalf("finish task1: %v", err)
	}

	// What this test's own tasks contribute to the aggregate.
	delta := protocol.TaskStats{
		Created:   2,
		Completed: 1,
		ByStatus: []protocol.TaskStatsStatusCount{
			{Status: protocol.TaskDone, Count: 1},
			{Status: protocol.TaskRunning, Count: 1},
		},
	}
	task1Only := protocol.TaskStats{
		Created:   1,
		Completed: 1,
		ByStatus:  []protocol.TaskStatsStatusCount{{Status: protocol.TaskDone, Count: 1}},
	}
	task2Only := protocol.TaskStats{
		Created:   1,
		Completed: 0,
		ByStatus:  []protocol.TaskStatsStatusCount{{Status: protocol.TaskRunning, Count: 1}},
	}
	empty := protocol.TaskStats{}

	cases := []struct {
		label string
		query string
		want  protocol.TaskStats
	}{
		{"unfiltered", "", mergeStats(base, delta)},
		{"windowed", "?from=" + from + "&to=" + to, mergeStats(baseWindow, delta)},
		{"workspace1", "?workspace_id=" + task1.WorkspaceID, task1Only},
		{"workspace2", "?workspace_id=" + task2.WorkspaceID, task2Only},
		{"node2", "?node_id=" + node2, task2Only},
		// Window edges are scoped to this test's nodes so leftover rows from
		// other tests cannot leak into the expected empty result.
		{"future from", "?node_id=" + node1 + "&from=" + futureFrom, empty},
		{"past to", "?node_id=" + node2 + "&to=" + pastTo, empty},
	}
	for _, tc := range cases {
		wantTaskStats(t, tc.label, get("GET /api/stats/tasks "+tc.label, tc.query), tc.want)
	}

	// An explicit filter that resolves to nothing visible is a 404; a malformed
	// time bound is a 400.
	resp, raw := getRaw(t, auth.token, statsURL+"?workspace_id="+randomUUID(t))
	requireStatus(t, "unknown workspace filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, auth.token, statsURL+"?node_id="+randomUUID(t))
	requireStatus(t, "unknown node filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, auth.token, statsURL+"?from=yesterday")
	requireStatus(t, "malformed from", resp, raw, http.StatusBadRequest)
	resp, raw = getRaw(t, auth.token, statsURL+"?to=tomorrow")
	requireStatus(t, "malformed to", resp, raw, http.StatusBadRequest)
}

// TestStatsTasksVisibility pins the binding scoping of the aggregate: a
// non-admin only counts tasks under a bound node; explicit filters naming
// invisible ids are 404 (no existence leak); unbinding empties the view.
func TestStatsTasksVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, nodeA := dialNode(t, ts, st, uniqueName("vis-taskstats-a"))
	defer func() { _ = c1.Close() }()
	c2, nodeB := dialNode(t, ts, st, uniqueName("vis-taskstats-b"))
	defer func() { _ = c2.Close() }()

	taskA := createWorkspaceAndTask(t, auth.token, ts, nodeA)
	taskB := createWorkspaceAndTask(t, auth.token, ts, nodeB)

	user, userToken := setupUserWithRole(t, st, "user")
	bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeA)

	statsURL := ts.URL + "/api/stats/tasks"
	get := func(label, query string) protocol.TaskStats {
		t.Helper()
		resp, raw := getRaw(t, userToken, statsURL+query)
		return decodeOK[protocol.TaskStats](t, label, resp, raw)
	}

	// Everything here is scoped to this test's unique nodes, so the expected
	// values are exact: one running task per node, nothing completed.
	subtreeOnly := protocol.TaskStats{
		Created:   1,
		Completed: 0,
		ByStatus:  []protocol.TaskStatsStatusCount{{Status: protocol.TaskRunning, Count: 1}},
	}
	empty := protocol.TaskStats{}

	wantTaskStats(t, "unfiltered", get("user stats", ""), subtreeOnly)

	// Explicit filters naming invisible ids are 404 even though they exist.
	resp, raw := getRaw(t, userToken, statsURL+"?node_id="+nodeB)
	requireStatus(t, "unbound node filter", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, userToken, statsURL+"?workspace_id="+taskB.WorkspaceID)
	requireStatus(t, "unbound workspace filter", resp, raw, http.StatusNotFound)

	// Explicit filters on the bound subtree resolve and still stay scoped.
	wantTaskStats(t, "bound node", get("bound node filter", "?node_id="+nodeA), subtreeOnly)
	wantTaskStats(t, "bound workspace", get("bound workspace filter", "?workspace_id="+taskA.WorkspaceID), subtreeOnly)

	// Unbinding empties the view and flips id filters back to 404.
	bindUserToNodes(t, ts.URL, auth.token, user.ID)
	wantTaskStats(t, "unbound", get("unbound stats", ""), empty)
	resp, raw = getRaw(t, userToken, statsURL+"?node_id="+nodeA)
	requireStatus(t, "unbound node filter after unbind", resp, raw, http.StatusNotFound)
}
