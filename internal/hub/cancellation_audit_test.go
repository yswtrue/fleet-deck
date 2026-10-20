package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"fleetdeck/internal/protocol"
)

func TestTaskCancellationAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires live Postgres")
	}
	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("cancel-audit"))
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	for _, initial := range []protocol.SessionStatus{protocol.SessionRunning, protocol.SessionAwaitingUser} {
		t.Run(string(initial), func(t *testing.T) {
			task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
			h.applySessionStatus(ctx, task.SessionID, initial)
			resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"status": "done"})
			requireStatus(t, "manual termination", resp, raw, http.StatusOK)
			var cancelled protocol.Task
			if err := json.Unmarshal(raw, &cancelled); err != nil {
				t.Fatal(err)
			}
			if cancelled.Status != protocol.TaskDone || cancelled.CancelledAt == nil {
				t.Fatalf("missing audit: %+v", cancelled)
			}
			first := *cancelled.CancelledAt
			// Idempotent termination and later lifecycle changes must not erase or
			// rewrite the first cancellation timestamp.
			resp, raw = patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"status": "done"})
			requireStatus(t, "repeat termination", resp, raw, http.StatusOK)
			h.applySessionStatus(ctx, task.SessionID, protocol.SessionRunning)
			h.applySessionStatus(ctx, task.SessionID, protocol.SessionDone)
			resp, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+task.WorkspaceID)
			requireStatus(t, "read audit", resp, raw, http.StatusOK)
			var tasks []protocol.Task
			if err := json.Unmarshal(raw, &tasks); err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 1 || tasks[0].CancelledAt == nil || !tasks[0].CancelledAt.Equal(first) {
				t.Fatalf("audit changed: %+v", tasks)
			}
		})
	}
	for _, terminal := range []protocol.SessionStatus{protocol.SessionDone, protocol.SessionFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
			h.applySessionStatus(ctx, task.SessionID, terminal)
			resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"status": "done"})
			requireStatus(t, "idempotent natural completion", resp, raw, http.StatusOK)
			stored, err := st.Queries().GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != protocol.TaskDone || stored.CancelledAt != nil {
				t.Fatalf("natural completion recorded as cancellation: %+v", stored)
			}
		})
	}
}
