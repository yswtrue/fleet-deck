package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"fleetdeck/internal/protocol"
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

// TestSmokeCRUDRoundTrip exercises one query per table against the live
// database. It skips (rather than fails) when Postgres is not reachable, so it
// is safe to run in environments without the devenv services up.
func TestSmokeCRUDRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("store smoke test needs a live Postgres; skipped under -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := New(ctx, testDSN(t))
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	// Registered first so it runs last: t.Cleanup is LIFO, which keeps the pool
	// open for the row cleanup registered below.
	t.Cleanup(st.Close)
	q := st.Queries()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// node -------------------------------------------------------------------
	node, err := q.CreateNode(ctx, CreateNodeParams{
		Name:         "smoke-" + suffix,
		Host:         "test-host",
		Status:       protocol.NodeOnline,
		Capabilities: []string{"fake"},
		TokenHash:    "hash-" + suffix,
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// Deleting the node cascades to every row created below.
	t.Cleanup(func() {
		bg, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if _, err := st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID); err != nil {
			t.Logf("cleanup: delete node: %v", err)
		}
	})

	if got, err := q.GetNode(ctx, node.ID); err != nil || got.ID != node.ID {
		t.Fatalf("GetNode: %v (id %q)", err, got.ID)
	}
	if got, err := q.GetNodeByName(ctx, node.Name); err != nil || got.ID != node.ID {
		t.Fatalf("GetNodeByName: %v (id %q)", err, got.ID)
	}
	if got, err := q.GetNodeByTokenHash(ctx, "hash-"+suffix); err != nil || got.ID != node.ID {
		t.Fatalf("GetNodeByTokenHash: %v (id %q)", err, got.ID)
	}
	if err := q.UpdateNodeTokenHash(ctx, node.ID, "hash2-"+suffix); err != nil {
		t.Fatalf("UpdateNodeTokenHash: %v", err)
	}
	if err := q.UpdateNodeStatus(ctx, node.ID, protocol.NodeOffline, time.Now()); err != nil {
		t.Fatalf("UpdateNodeStatus: %v", err)
	}
	if nodes, err := q.ListNodes(ctx); err != nil || len(nodes) == 0 {
		t.Fatalf("ListNodes: %v (%d rows)", err, len(nodes))
	}

	// workspace --------------------------------------------------------------
	ws, err := q.CreateWorkspace(ctx, CreateWorkspaceParams{NodeID: node.ID, Name: "ws-" + suffix, Path: "/tmp/ws"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if got, err := q.GetWorkspace(ctx, ws.ID); err != nil || got.NodeID != node.ID {
		t.Fatalf("GetWorkspace: %v (node %q)", err, got.NodeID)
	}
	if list, err := q.ListWorkspacesByNode(ctx, node.ID); err != nil || len(list) == 0 {
		t.Fatalf("ListWorkspacesByNode: %v (%d rows)", err, len(list))
	}

	// task -------------------------------------------------------------------
	task, err := q.CreateTask(ctx, CreateTaskParams{
		WorkspaceID: ws.ID,
		Title:       "task-" + suffix,
		Prompt:      "do the thing",
		Status:      protocol.TaskWaiting,
		AgentType:   "fake",
		SortOrder:   1,
		AutoStart:   true,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if !task.AutoStart || task.ArchivedAt != nil {
		t.Fatalf("CreateTask lifecycle defaults: auto_start=%v archived_at=%v", task.AutoStart, task.ArchivedAt)
	}
	backlog, err := q.CreateTask(ctx, CreateTaskParams{
		WorkspaceID: ws.ID,
		Title:       "backlog-" + suffix,
		Prompt:      "later",
		Status:      protocol.TaskWaiting,
		AgentType:   "fake",
		AutoStart:   false,
	})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	if backlog.AutoStart {
		t.Fatalf("backlog task auto_start = true, want false")
	}
	archivedAt := time.Now().UTC()
	if got, err := q.UpdateTaskArchivedAt(ctx, task.ID, &archivedAt); err != nil || got.ArchivedAt == nil {
		t.Fatalf("UpdateTaskArchivedAt: %v (%+v)", err, got)
	}
	if list, err := q.ListTasksByWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("ListTasksByWorkspace: %v", err)
	} else {
		for _, row := range list {
			if row.ID == task.ID {
				t.Fatalf("archived task %s leaked into the default list", task.ID)
			}
		}
	}
	if list, err := q.ListArchivedTasksByWorkspace(ctx, ws.ID); err != nil || len(list) != 1 || list[0].ID != task.ID {
		t.Fatalf("ListArchivedTasksByWorkspace: %v (%d rows)", err, len(list))
	}
	if got, err := q.UpdateTaskArchivedAt(ctx, task.ID, nil); err != nil || got.ArchivedAt != nil {
		t.Fatalf("unarchive: %v (%+v)", err, got)
	}
	if got, err := q.GetTask(ctx, task.ID); err != nil || got.WorkspaceID != ws.ID {
		t.Fatalf("GetTask: %v (workspace %q)", err, got.WorkspaceID)
	}
	if list, err := q.ListTasksByWorkspace(ctx, ws.ID); err != nil || len(list) == 0 {
		t.Fatalf("ListTasksByWorkspace: %v (%d rows)", err, len(list))
	}
	if err := q.UpdateTaskSortOrder(ctx, task.ID, 7); err != nil {
		t.Fatalf("UpdateTaskSortOrder: %v", err)
	}
	if got, err := q.UpdateTaskStatus(ctx, task.ID, protocol.TaskRunning); err != nil || got.Status != protocol.TaskRunning {
		t.Fatalf("UpdateTaskStatus: %v (status %q)", err, got.Status)
	}

	// session ----------------------------------------------------------------
	sess, err := q.CreateSession(ctx, CreateSessionParams{
		TaskID:    task.ID,
		NodeID:    node.ID,
		AgentType: "fake",
		Status:    protocol.SessionRunning,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := q.UpdateTaskSessionID(ctx, task.ID, sess.ID); err != nil {
		t.Fatalf("UpdateTaskSessionID: %v", err)
	}
	if got, err := q.GetTaskBySessionID(ctx, sess.ID); err != nil || got.ID != task.ID {
		t.Fatalf("GetTaskBySessionID: %v (id %q)", err, got.ID)
	}
	if err := q.UpdateSessionExternalID(ctx, sess.ID, "ext-"+suffix); err != nil {
		t.Fatalf("UpdateSessionExternalID: %v", err)
	}
	if got, err := q.GetSession(ctx, sess.ID); err != nil || got.ExternalSessionID != "ext-"+suffix {
		t.Fatalf("GetSession: %v (ext %q)", err, got.ExternalSessionID)
	}
	ended := time.Now()
	if err := q.UpdateSessionStatus(ctx, sess.ID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("UpdateSessionStatus: %v", err)
	}
	if list, err := q.ListSessionsByNode(ctx, node.ID); err != nil || len(list) == 0 {
		t.Fatalf("ListSessionsByNode: %v (%d rows)", err, len(list))
	}
	if list, err := q.ListSessionsByTask(ctx, task.ID); err != nil || len(list) == 0 {
		t.Fatalf("ListSessionsByTask: %v (%d rows)", err, len(list))
	}

	// subtask ----------------------------------------------------------------
	if _, err := q.UpsertSubtaskByExternalID(ctx, UpsertSubtaskByExternalIDParams{
		TaskID: task.ID, Title: "todo", Status: protocol.SubtaskPending, ExternalID: "t1", SortOrder: 1,
	}); err != nil {
		t.Fatalf("UpsertSubtaskByExternalID (insert): %v", err)
	}
	up, err := q.UpsertSubtaskByExternalID(ctx, UpsertSubtaskByExternalIDParams{
		TaskID: task.ID, Title: "todo done", Status: protocol.SubtaskDone, ExternalID: "t1", SortOrder: 2,
	})
	if err != nil || up.Status != protocol.SubtaskDone {
		t.Fatalf("UpsertSubtaskByExternalID (update): %v (status %q)", err, up.Status)
	}
	if list, err := q.ListSubtasksByTask(ctx, task.ID); err != nil || len(list) != 1 {
		t.Fatalf("ListSubtasksByTask: %v (%d rows)", err, len(list))
	}
	if err := q.DeleteSubtaskByExternalID(ctx, task.ID, "t1"); err != nil {
		t.Fatalf("DeleteSubtaskByExternalID: %v", err)
	}

	// event ------------------------------------------------------------------
	if _, err := q.AppendEvent(ctx, sess.ID, "smoke", []byte(`{"ok":true}`), 1); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if seq, err := q.MaxEventSeq(ctx, sess.ID); err != nil || seq != 1 {
		t.Fatalf("MaxEventSeq: %v (got %d, want 1)", err, seq)
	}
	if list, err := q.ListEventsBySession(ctx, sess.ID, 0, 10); err != nil || len(list) != 1 {
		t.Fatalf("ListEventsBySession: %v (%d rows)", err, len(list))
	}

	// transaction ------------------------------------------------------------
	if err := st.WithTx(ctx, func(tx *Queries) error {
		return tx.UpdateNodeStatus(ctx, node.ID, protocol.NodeOnline, time.Now())
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if err := st.WithTx(ctx, func(tx *Queries) error {
		return fmt.Errorf("intentional rollback")
	}); err == nil {
		t.Fatal("WithTx: expected error to propagate")
	}
}
