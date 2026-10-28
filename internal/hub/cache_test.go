package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// TestTaskListCacheInvalidation pins the board cache contract: reads are
// served from cache, and every task mutation (create here; status changes are
// exercised by the suite at large through the same cache) invalidates it so
// the next read sees fresh rows.
func TestTaskListCacheInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("cache-node"))
	defer func() { _ = c.Close() }()

	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("cache-ws"), "path": "/tmp/cache-ws",
	})
	first := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": uniqueName("cache-t1"), "prompt": "p", "agent_type": "fake",
	})

	listTasks := func() []protocol.Task {
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws.ID)
		requireStatus(t, "list tasks", resp, raw, http.StatusOK)
		var tasks []protocol.Task
		if err := json.Unmarshal(raw, &tasks); err != nil {
			t.Fatalf("decode tasks: %v (%s)", err, raw)
		}
		return tasks
	}

	if tasks := listTasks(); len(tasks) != 1 {
		t.Fatalf("after create: %d tasks, want 1", len(tasks))
	}

	// A second create must invalidate the cached list. Resolve the first task
	// first: a workspace runs one task at a time, so the second would queue
	// (and it must be running for the status change below to be legal).
	if resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+first.ID, map[string]any{"status": "done"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch first status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	// The workspace slot is only free once the session is terminal too (the
	// queue treats a still-live agent as occupied).
	if err := st.Queries().UpdateSessionStatus(context.Background(), first.SessionID, protocol.SessionDone, nil); err != nil {
		t.Fatalf("end first session: %v", err)
	}
	second := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": uniqueName("cache-t2"), "prompt": "p", "agent_type": "fake",
	})
	if second.SessionID == "" {
		t.Fatalf("second task was queued instead of dispatched: %+v", second)
	}
	if tasks := listTasks(); len(tasks) != 2 {
		t.Fatalf("after second create: %d tasks (stale cache?), want 2", len(tasks))
	}

	// A status change must invalidate it too.
	if resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+second.ID, map[string]any{"status": "done"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	for _, task := range listTasks() {
		if task.ID == second.ID && task.Status != protocol.TaskDone {
			t.Fatalf("task status = %s after patch (stale cache?), want done", task.Status)
		}
	}
}

// TestAuthCacheLogoutRevokesCachedToken: a token resolved into the cache must
// stop working the moment logout revokes it — the cached entry is deleted, not
// left to expire.
func TestAuthCacheLogoutRevokesCachedToken(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, _, ts, auth := newTestHub(t)

	// Warm the token cache.
	if resp, raw := getRaw(t, auth.token, ts.URL+"/api/auth/me"); resp.StatusCode != http.StatusOK {
		t.Fatalf("warm auth/me = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	if resp, raw := postRaw(t, auth.token, ts.URL+"/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204; body=%s", resp.StatusCode, raw)
	}

	// The same token must be rejected immediately despite the cache entry.
	if resp, raw := getRaw(t, auth.token, ts.URL+"/api/auth/me"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth/me after logout = %d, want 401 (stale auth cache); body=%s", resp.StatusCode, raw)
	}
}

// TestCacheOutageDegradesToDatabase: a configured but unreachable Redis must
// not fail requests — every cache error is a miss.
func TestCacheOutageDegradesToDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	st, err := store.New(ctx, testDSN(t))
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	t.Cleanup(st.Close)

	// Port 1 is never a live Redis: every operation errors.
	c, err := cache.NewRedis("redis://127.0.0.1:1")
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}
	h := New(ctx, st, adapters.NewRegistry(), c)
	t.Cleanup(func() { _ = h.Close() })
	ts := httptest.NewServer(h.Handler())
	t.Cleanup(ts.Close)

	_, token := setupUserWithRole(t, st, "admin")
	for _, path := range []string{"/api/nodes", "/api/auth/me"} {
		if resp, raw := getRaw(t, token, ts.URL+path); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s with dead redis = %d, want 200; body=%s", path, resp.StatusCode, raw)
		}
	}
}
