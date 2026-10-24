package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

func TestWorkspaceRenameUpdatesNameAndPreservesScope(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("rename-node"))
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID)
	})
	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": node.ID, "name": uniqueName("rename-ws"), "path": "/tmp/rename-ws",
	})

	resp, raw := patchRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID, map[string]any{
		"name": "  Renamed workspace  ",
	})
	requireStatus(t, "rename workspace", resp, raw, http.StatusOK)
	var renamed protocol.Workspace
	if err := json.Unmarshal(raw, &renamed); err != nil {
		t.Fatalf("decode renamed workspace: %v (%s)", err, raw)
	}
	if renamed.Name != "Renamed workspace" {
		t.Fatalf("renamed name = %q, want trimmed %q", renamed.Name, "Renamed workspace")
	}
	if renamed.AutoAllowScope != protocol.AutoAllowScopeNone {
		t.Fatalf("rename changed auto_allow_scope to %q", renamed.AutoAllowScope)
	}

	// The two PATCH fields are independent: each preserves the other.
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID, map[string]any{
		"auto_allow_scope": "all",
	})
	requireStatus(t, "set scope", resp, raw, http.StatusOK)
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID, map[string]any{
		"name": "Renamed again",
	})
	requireStatus(t, "rename preserves scope", resp, raw, http.StatusOK)
	if err := json.Unmarshal(raw, &renamed); err != nil {
		t.Fatalf("decode second rename: %v (%s)", err, raw)
	}
	if renamed.Name != "Renamed again" || renamed.AutoAllowScope != protocol.AutoAllowScopeAll {
		t.Fatalf("workspace after rename = %+v, want name=Renamed again scope=all", renamed)
	}

	for _, tc := range []struct {
		name string
		body any
		want string
	}{
		{"blank name", map[string]any{"name": "   "}, "name is required"},
		{"empty patch", map[string]any{}, "name or auto_allow_scope is required"},
		{"invalid scope", map[string]any{"auto_allow_scope": "everywhere"}, "invalid auto_allow_scope"},
	} {
		resp, raw = patchRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID, tc.body)
		requireStatus(t, tc.name, resp, raw, http.StatusBadRequest)
		if got := decodeErr(t, raw); got != tc.want {
			t.Fatalf("%s error = %q, want %q", tc.name, got, tc.want)
		}
	}

	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/workspaces/"+randomUUID(t), map[string]any{
		"name": "missing",
	})
	requireStatus(t, "rename missing workspace", resp, raw, http.StatusNotFound)
}

func TestWorkspaceDeleteCascadesAndRefusesActiveSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("delete-node"))
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID)
	})
	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": node.ID, "name": uniqueName("delete-ws"), "path": "/tmp/delete-ws",
	})
	task := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]any{
		"workspace_id": ws.ID,
		"title":        uniqueName("delete-task"),
		"prompt":       "p",
		"agent_type":   "fake",
		"auto_start":   false,
	})

	ctx := context.Background()
	sess, err := st.Queries().CreateSession(ctx, store.CreateSessionParams{
		TaskID:    task.ID,
		NodeID:    node.ID,
		AgentType: "fake",
		Status:    protocol.SessionRunning,
	})
	if err != nil {
		t.Fatalf("seed running session: %v", err)
	}

	resp, raw := deleteReq(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "delete workspace with active session", resp, raw, http.StatusConflict)
	if got := decodeErr(t, raw); got != "workspace has active sessions; finish or cancel them first" {
		t.Fatalf("active-session error = %q", got)
	}
	if _, err := st.Queries().GetWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("workspace disappeared after refused delete: %v", err)
	}

	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, sess.ID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("finish session: %v", err)
	}
	resp, raw = deleteReq(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "delete finished workspace", resp, raw, http.StatusNoContent)

	if _, err := st.Queries().GetWorkspace(ctx, ws.ID); err == nil {
		t.Fatal("workspace still exists after delete")
	}
	if _, err := st.Queries().GetTask(ctx, task.ID); err == nil {
		t.Fatal("task survived workspace delete (cascade missing)")
	}
	if _, err := st.Queries().GetSession(ctx, sess.ID); err == nil {
		t.Fatal("session survived workspace delete (cascade missing)")
	}
	resp, raw = getRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "get deleted workspace", resp, raw, http.StatusNotFound)
}

func TestWorkspaceRenameDeleteRequiresBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("bind-write-node"))
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID)
	})
	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": node.ID, "name": uniqueName("bind-write-ws"), "path": "/tmp/bind-write-ws",
	})
	user, userToken := setupUserWithRole(t, st, "user")

	resp, raw := patchRaw(t, userToken, ts.URL+"/api/workspaces/"+ws.ID, map[string]any{
		"name": "not allowed",
	})
	requireStatus(t, "rename unbound workspace", resp, raw, http.StatusNotFound)
	resp, raw = deleteReq(t, userToken, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "delete unbound workspace", resp, raw, http.StatusNotFound)
	if _, err := st.Queries().GetWorkspace(context.Background(), ws.ID); err != nil {
		t.Fatalf("unbound delete removed workspace: %v", err)
	}

	bindUserToNodes(t, ts.URL, auth.token, user.ID, node.ID)
	resp, raw = patchRaw(t, userToken, ts.URL+"/api/workspaces/"+ws.ID, map[string]any{
		"name": "bound rename",
	})
	requireStatus(t, "rename bound workspace", resp, raw, http.StatusOK)
	resp, raw = deleteReq(t, userToken, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "delete bound workspace", resp, raw, http.StatusNoContent)
}
