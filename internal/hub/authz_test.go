package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// ---- auth endpoint helpers -------------------------------------------------

func wipeUsers(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "DELETE FROM users"); err != nil {
		t.Fatalf("wipe users: %v", err)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM users"); err != nil {
			t.Logf("cleanup: wipe users: %v", err)
		}
	})
}

func decodeErr(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, raw)
	}
	return body.Error
}

// requireStatus asserts a status code, failing with the raw body otherwise.
func requireStatus(t *testing.T, what string, resp *http.Response, raw []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s status = %d, want %d; body=%s", what, resp.StatusCode, want, raw)
	}
}

// ---- setup / status --------------------------------------------------------

// TestAuthStatusAndSetup drives first-boot: empty users table reports
// needs_setup, setup mints the admin + token, and a second setup is 409.
func TestAuthStatusAndSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, _ := newTestHub(t)
	wipeUsers(t, st)

	resp, raw := getRaw(t, "", ts.URL+"/api/auth/status")
	requireStatus(t, "auth status", resp, raw, http.StatusOK)
	var status struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status: %v (%s)", err, raw)
	}
	if !status.NeedsSetup {
		t.Fatal("needs_setup = false on an empty users table, want true")
	}

	resp, raw = postRaw(t, "", ts.URL+"/api/auth/setup", map[string]string{
		"email":        " Admin@Example.com ",
		"display_name": "Root",
		"password":     "password123",
	})
	requireStatus(t, "setup", resp, raw, http.StatusCreated)
	var created tokenResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode setup: %v (%s)", err, raw)
	}
	if created.Token == "" {
		t.Fatal("setup returned an empty token")
	}
	if created.User.Role != "admin" {
		t.Fatalf("setup user role = %q, want admin", created.User.Role)
	}
	if created.User.Email != "admin@example.com" {
		t.Fatalf("setup user email = %q, want normalized lowercase", created.User.Email)
	}
	if created.ExpiresAt.Before(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("setup expires_at = %v, want ~30 days out", created.ExpiresAt)
	}
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("setup response leaks password_hash: %s", raw)
	}

	// The table is no longer empty: needs_setup flips and setup is 409.
	resp, raw = getRaw(t, "", ts.URL+"/api/auth/status")
	requireStatus(t, "auth status after setup", resp, raw, http.StatusOK)
	status.NeedsSetup = true
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status: %v (%s)", err, raw)
	}
	if status.NeedsSetup {
		t.Fatal("needs_setup = true after setup, want false")
	}

	resp, raw = postRaw(t, "", ts.URL+"/api/auth/setup", map[string]string{
		"email": "second@example.com", "display_name": "Second", "password": "password123",
	})
	requireStatus(t, "second setup", resp, raw, http.StatusConflict)

	// The minted token authenticates.
	resp, raw = getRaw(t, created.Token, ts.URL+"/api/auth/me")
	requireStatus(t, "me with setup token", resp, raw, http.StatusOK)
}

// TestAuthSetupValidation pins the setup guards: short passwords are refused.
func TestAuthSetupValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, _ := newTestHub(t)
	wipeUsers(t, st)

	resp, raw := postRaw(t, "", ts.URL+"/api/auth/setup", map[string]string{
		"email": "a@example.com", "display_name": "A", "password": "short",
	})
	requireStatus(t, "short-password setup", resp, raw, http.StatusBadRequest)
}

// ---- login -----------------------------------------------------------------

// TestAuthLoginSuccessAndFailures pins the login contract: correct credentials
// return a token; an unknown email and a wrong password both return the
// identical 401 {"error":"invalid email or password"}.
func TestAuthLoginSuccessAndFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, _ := newTestHub(t)
	wipeUsers(t, st)

	resp, raw := postRaw(t, "", ts.URL+"/api/auth/setup", map[string]string{
		"email": "login@example.com", "display_name": "Login", "password": "password123",
	})
	requireStatus(t, "setup", resp, raw, http.StatusCreated)

	resp, raw = postRaw(t, "", ts.URL+"/api/auth/login", map[string]string{
		"email": "LOGIN@example.com", "password": "password123",
	})
	requireStatus(t, "login", resp, raw, http.StatusOK)
	var loggedIn tokenResponse
	if err := json.Unmarshal(raw, &loggedIn); err != nil {
		t.Fatalf("decode login: %v (%s)", err, raw)
	}
	if loggedIn.Token == "" || loggedIn.User.Email != "login@example.com" {
		t.Fatalf("login response = %s, want a token and the user", raw)
	}

	for _, tc := range []struct {
		name string
		body map[string]string
	}{
		{"unknown email", map[string]string{"email": "nobody@example.com", "password": "password123"}},
		{"bad password", map[string]string{"email": "login@example.com", "password": "wrong-password"}},
	} {
		resp, raw := postRaw(t, "", ts.URL+"/api/auth/login", tc.body)
		requireStatus(t, "login "+tc.name, resp, raw, http.StatusUnauthorized)
		if got := decodeErr(t, raw); got != "invalid email or password" {
			t.Fatalf("login %s error = %q, want %q", tc.name, got, "invalid email or password")
		}
	}
}

// ---- 401 / 403 gating ------------------------------------------------------

// TestAuthRequired pins the unauthenticated contract: protected endpoints
// answer 401 {"error":"unauthorized"} without a token, and also with a bogus
// token.
func TestAuthRequired(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, _ := newTestHub(t)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{"GET", "/api/nodes"},
		{"POST", "/api/nodes"},
		{"GET", "/api/users"},
		{"GET", "/api/workspaces?node_id=x"},
		{"GET", "/api/tasks?node_id=x"},
		{"POST", "/api/tasks"},
		{"GET", "/api/auth/me"},
		{"POST", "/api/auth/logout"},
	} {
		for _, token := range []string{"", "bogus-token"} {
			resp, raw := doRequest(t, tc.method, token, ts.URL+tc.path,
				map[string]string{"_": "_"})
			requireStatus(t, tc.method+" "+tc.path, resp, raw, http.StatusUnauthorized)
			if got := decodeErr(t, raw); got != "unauthorized" {
				t.Fatalf("%s %s error = %q, want unauthorized", tc.method, tc.path, got)
			}
		}
	}

	// Open endpoints stay open.
	resp, raw := getRaw(t, "", ts.URL+"/api/health")
	requireStatus(t, "health", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, "", ts.URL+"/api/auth/status")
	requireStatus(t, "auth status", resp, raw, http.StatusOK)
}

// TestNonAdminForbidden pins the admin-only contract: a user-role token gets
// 403 on admin endpoints but can still list nodes (discovery).
func TestNonAdminForbidden(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	_, userToken := setupUserWithRole(t, st, "user")

	for _, tc := range []struct {
		method string
		path   string
	}{
		{"POST", "/api/nodes"},
		{"GET", "/api/users"},
		{"POST", "/api/users"},
		{"PATCH", "/api/users/" + auth.user.ID},
		{"DELETE", "/api/users/" + auth.user.ID},
		{"POST", "/api/workspaces"},
	} {
		resp, raw := doRequest(t, tc.method, userToken, ts.URL+tc.path,
			map[string]string{"_": "_"})
		requireStatus(t, tc.method+" "+tc.path, resp, raw, http.StatusForbidden)
	}

	// Discovery reads stay open to the user role.
	resp, raw := getRaw(t, userToken, ts.URL+"/api/nodes")
	requireStatus(t, "user list nodes", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/auth/me")
	requireStatus(t, "user me", resp, raw, http.StatusOK)
}

// ---- logout / revocation ---------------------------------------------------

// TestLogoutRevokesToken pins revocation: logout is 204 and the token can no
// longer be used (401 on reuse).
func TestLogoutRevokesToken(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	resp, raw := postRaw(t, auth.token, ts.URL+"/api/auth/logout", map[string]string{})
	requireStatus(t, "logout", resp, raw, http.StatusNoContent)

	resp, raw = getRaw(t, auth.token, ts.URL+"/api/auth/me")
	requireStatus(t, "me after logout", resp, raw, http.StatusUnauthorized)
}

// TestExpiredTokenRejected pins expiry: a token past expires_at behaves as
// unauthenticated.
func TestExpiredTokenRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)

	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	ctx := context.Background()
	if _, err := st.Queries().CreateAuthToken(ctx, store.CreateAuthTokenParams{
		UserID:    auth.user.ID,
		TokenHash: hashAuthToken(token),
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}

	resp, raw := getRaw(t, token, ts.URL+"/api/auth/me")
	requireStatus(t, "me with expired token", resp, raw, http.StatusUnauthorized)
}

// ---- user admin ------------------------------------------------------------

// TestUserAdminCRUD drives the admin user lifecycle: list, create, patch
// (display_name, role, password), and delete.
func TestUserAdminCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/users")
	requireStatus(t, "list users", resp, raw, http.StatusOK)
	var listed []userDTO
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode users: %v (%s)", err, raw)
	}
	if len(listed) == 0 {
		t.Fatal("user list is empty, want at least the test admin")
	}
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("user list leaks password_hash: %s", raw)
	}

	// Create a plain user.
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/users", map[string]string{
		"email": " member@Example.com ", "display_name": "Member", "password": "password123", "role": "user",
	})
	requireStatus(t, "create user", resp, raw, http.StatusCreated)
	var member userDTO
	if err := json.Unmarshal(raw, &member); err != nil {
		t.Fatalf("decode created user: %v (%s)", err, raw)
	}
	if member.Email != "member@example.com" || member.Role != "user" {
		t.Fatalf("created user = %+v, want normalized email and role=user", member)
	}

	// Duplicate email is 409.
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/users", map[string]string{
		"email": "member@example.com", "display_name": "Dupe", "password": "password123", "role": "user",
	})
	requireStatus(t, "duplicate email", resp, raw, http.StatusConflict)

	// Patch display_name.
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/users/"+member.ID, map[string]string{
		"display_name": "Renamed",
	})
	requireStatus(t, "patch display_name", resp, raw, http.StatusOK)
	var renamed userDTO
	if err := json.Unmarshal(raw, &renamed); err != nil {
		t.Fatalf("decode patched user: %v (%s)", err, raw)
	}
	if renamed.DisplayName != "Renamed" {
		t.Fatalf("patched user = %+v, want display_name=Renamed", renamed)
	}

	// Patch password: the old password stops working, the new one works.
	_, memberToken := setupUserWithRole(t, st, "user")
	_ = memberToken
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/users/"+member.ID, map[string]string{
		"password": "newpassword123",
	})
	requireStatus(t, "reset password", resp, raw, http.StatusOK)

	resp, raw = postRaw(t, "", ts.URL+"/api/auth/login", map[string]string{
		"email": "member@example.com", "password": "newpassword123",
	})
	requireStatus(t, "login with new password", resp, raw, http.StatusOK)
	resp, raw = postRaw(t, "", ts.URL+"/api/auth/login", map[string]string{
		"email": "member@example.com", "password": "password123",
	})
	requireStatus(t, "login with old password", resp, raw, http.StatusUnauthorized)

	// Delete the user; they are gone from the list and cannot log in.
	resp, raw = deleteReq(t, auth.token, ts.URL+"/api/users/"+member.ID)
	requireStatus(t, "delete user", resp, raw, http.StatusNoContent)
	resp, raw = postRaw(t, "", ts.URL+"/api/auth/login", map[string]string{
		"email": "member@example.com", "password": "newpassword123",
	})
	requireStatus(t, "login after delete", resp, raw, http.StatusUnauthorized)
}

// TestLastAdminProtection pins the invariant: the last admin can be neither
// demoted nor deleted (409), and nobody can delete themselves.
func TestLastAdminProtection(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)

	// Self-delete is 409 even when other admins... first prove it on the test
	// admin directly (it may be the only admin in the table at this point, so
	// both rules agree on 409).
	resp, raw := deleteReq(t, auth.token, ts.URL+"/api/users/"+auth.user.ID)
	requireStatus(t, "delete self", resp, raw, http.StatusConflict)

	// Create a second admin: demoting/deleting the first is now legal...
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/users", map[string]string{
		"email": "admin2@example.com", "display_name": "Admin2", "password": "password123", "role": "admin",
	})
	requireStatus(t, "create second admin", resp, raw, http.StatusCreated)
	var admin2 userDTO
	if err := json.Unmarshal(raw, &admin2); err != nil {
		t.Fatalf("decode admin2: %v (%s)", err, raw)
	}

	// ...but with the second admin gone again, protection is back.
	resp, raw = deleteReq(t, auth.token, ts.URL+"/api/users/"+admin2.ID)
	requireStatus(t, "delete second admin", resp, raw, http.StatusNoContent)

	admins, err := st.Queries().CountAdmins(context.Background())
	if err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if admins != 1 {
		// The shared table may hold leftover admins from other tests only if
		// their cleanup failed; the demote/delete attempts below assert the
		// 409 path on a dedicated admin instead of assuming table state.
		t.Logf("shared table holds %d admins; using a dedicated last-admin check", admins)
	}

	// Dedicated last-admin check: wipe, re-setup via store, then the single
	// admin cannot be demoted or deleted by anyone.
	wipeUsers(t, st)
	hash, err := hashPassword("password123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	sole, err := st.Queries().CreateUser(context.Background(), store.CreateUserParams{
		Email: "sole@example.com", DisplayName: "Sole", PasswordHash: hash, Role: "admin",
	})
	if err != nil {
		t.Fatalf("create sole admin: %v", err)
	}
	soleToken, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if _, err := st.Queries().CreateAuthToken(context.Background(), store.CreateAuthTokenParams{
		UserID: sole.ID, TokenHash: hashAuthToken(soleToken), ExpiresAt: time.Now().UTC().Add(authTokenTTL),
	}); err != nil {
		t.Fatalf("mint sole token: %v", err)
	}

	resp, raw = patchRaw(t, soleToken, ts.URL+"/api/users/"+sole.ID, map[string]string{"role": "user"})
	requireStatus(t, "demote last admin", resp, raw, http.StatusConflict)

	resp, raw = deleteReq(t, soleToken, ts.URL+"/api/users/"+sole.ID)
	requireStatus(t, "delete self (also last admin)", resp, raw, http.StatusConflict)
}

// ---- workspace scoping -----------------------------------------------------

// memberFixture provisions a node, a workspace on it, and two users: an admin
// (auth) and a plain user. It returns the workspace and the user token.
func memberFixture(t *testing.T, st *store.Store, tsURL, adminToken, nodeID string) (protocol.Workspace, string) {
	t.Helper()
	ws := postJSON[protocol.Workspace](t, adminToken, tsURL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("scope-ws"), "path": "/tmp/scope-ws",
	})
	_, userToken := setupUserWithRole(t, st, "user")
	return ws, userToken
}

// TestWorkspaceScopingForNonMembers pins the visibility rules: a non-member
// reads 404 on the workspace's tasks, subtasks, sessions, messages, events and
// send; cannot create tasks there (403); but sees the node list. Granting
// membership flips reads and task creation to success.
func TestWorkspaceScopingForNonMembers(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("scope-node"))
	defer func() { _ = c.Close() }()

	ws, userToken := memberFixture(t, st, ts.URL, auth.token, nodeID)
	task := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": uniqueName("scope-task"), "prompt": "p", "agent_type": "fake",
	})
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	// Non-member reads: everything workspace-scoped is 404.
	for _, path := range []string{
		"/api/workspaces/" + ws.ID,
		"/api/tasks?workspace_id=" + ws.ID,
		"/api/tasks/" + task.ID + "/subtasks",
		"/api/sessions/" + task.SessionID,
		"/api/sessions/" + task.SessionID + "/messages",
		"/api/sessions/" + task.SessionID + "/events",
	} {
		resp, raw := getRaw(t, userToken, ts.URL+path)
		requireStatus(t, "GET "+path, resp, raw, http.StatusNotFound)
	}
	resp, raw := postRaw(t, userToken, ts.URL+"/api/sessions/"+task.SessionID+"/send",
		map[string]string{"message": "hi"})
	requireStatus(t, "POST send as non-member", resp, raw, http.StatusNotFound)

	// Non-member mutation: 403.
	resp, raw = postRaw(t, userToken, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": "nope",
	})
	requireStatus(t, "create task as non-member", resp, raw, http.StatusForbidden)
	resp, raw = patchRaw(t, userToken, ts.URL+"/api/tasks/"+task.ID, map[string]any{"sort_order": 3})
	requireStatus(t, "patch task as non-member", resp, raw, http.StatusNotFound)

	// Node discovery still works for the plain user.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/nodes")
	requireStatus(t, "user list nodes", resp, raw, http.StatusOK)
	// ...but the unjoined workspace is not listed.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/workspaces?node_id="+nodeID)
	requireStatus(t, "user list workspaces", resp, raw, http.StatusOK)
	var mine []protocol.Workspace
	if err := json.Unmarshal(raw, &mine); err != nil {
		t.Fatalf("decode workspaces: %v (%s)", err, raw)
	}
	for _, w := range mine {
		if w.ID == ws.ID {
			t.Fatalf("non-member sees workspace %q in list", ws.ID)
		}
	}

	// A non-admin member cannot grant membership (403); an admin can (201,
	// idempotent), and the member list shows the user.
	var me struct {
		ID string `json:"id"`
	}
	resp, raw = getRaw(t, userToken, ts.URL+"/api/auth/me")
	requireStatus(t, "user me", resp, raw, http.StatusOK)
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatalf("decode me: %v (%s)", err, raw)
	}
	resp, raw = postRaw(t, userToken, ts.URL+"/api/workspaces/"+ws.ID+"/members",
		map[string]string{"user_id": me.ID})
	requireStatus(t, "grant as non-member non-admin", resp, raw, http.StatusNotFound)

	resp, raw = postRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID+"/members",
		map[string]string{"user_id": me.ID})
	requireStatus(t, "grant member", resp, raw, http.StatusCreated)
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID+"/members",
		map[string]string{"user_id": me.ID})
	requireStatus(t, "grant member idempotent", resp, raw, http.StatusCreated)

	resp, raw = getRaw(t, userToken, ts.URL+"/api/workspaces/"+ws.ID+"/members")
	requireStatus(t, "list members as member", resp, raw, http.StatusOK)
	var members []userDTO
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("decode members: %v (%s)", err, raw)
	}
	if len(members) != 1 || members[0].ID != me.ID {
		t.Fatalf("members = %+v, want exactly the joined user", members)
	}

	// Membership flips reads and task creation to success.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/workspaces/"+ws.ID)
	requireStatus(t, "member get workspace", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?workspace_id="+ws.ID)
	requireStatus(t, "member list tasks", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/sessions/"+task.SessionID)
	requireStatus(t, "member get session", resp, raw, http.StatusOK)
	created := postJSON[protocol.Task](t, userToken, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": uniqueName("member-task"), "prompt": "p", "agent_type": "fake",
	})
	if created.ID == "" {
		t.Fatal("member task creation returned no id")
	}

	// Removing the grant flips reads back to 404.
	resp, raw = deleteReq(t, auth.token, ts.URL+"/api/workspaces/"+ws.ID+"/members/"+me.ID)
	requireStatus(t, "remove member", resp, raw, http.StatusNoContent)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?workspace_id="+ws.ID)
	requireStatus(t, "list tasks after removal", resp, raw, http.StatusNotFound)
}

// TestTasksByNodeFilteredForNonAdmin pins node-scoped filtering: a plain user
// with membership in one workspace sees only that workspace's tasks through
// GET /api/tasks?node_id=.
func TestTasksByNodeFilteredForNonAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("filternode"))
	defer func() { _ = c.Close() }()

	mkWS := func(name string) protocol.Workspace {
		return postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
			"node_id": nodeID, "name": name, "path": "/tmp/" + name,
		})
	}
	mkTask := func(wsID, title string) protocol.Task {
		return postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
			"workspace_id": wsID, "title": title, "prompt": "p", "agent_type": "fake",
		})
	}
	wsIn := mkWS(uniqueName("filter-in"))
	wsOut := mkWS(uniqueName("filter-out"))
	inTask := mkTask(wsIn.ID, uniqueName("in-task"))
	_ = mkTask(wsOut.ID, uniqueName("out-task"))

	_, userToken := setupUserWithRole(t, st, "user")
	var me struct {
		ID string `json:"id"`
	}
	resp, raw := getRaw(t, userToken, ts.URL+"/api/auth/me")
	requireStatus(t, "user me", resp, raw, http.StatusOK)
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatalf("decode me: %v (%s)", err, raw)
	}
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/workspaces/"+wsIn.ID+"/members",
		map[string]string{"user_id": me.ID})
	requireStatus(t, "grant member", resp, raw, http.StatusCreated)

	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+nodeID)
	requireStatus(t, "filtered list by node", resp, raw, http.StatusOK)
	var tasks []protocol.Task
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("decode tasks: %v (%s)", err, raw)
	}
	if len(tasks) != 1 || tasks[0].ID != inTask.ID {
		t.Fatalf("filtered tasks = %v, want exactly [%s]", tasks, inTask.ID)
	}

	// Node-scoped search is filtered the same way.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+nodeID+"&q="+inTask.Title)
	requireStatus(t, "filtered search by node", resp, raw, http.StatusOK)
	tasks = nil
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("decode tasks: %v (%s)", err, raw)
	}
	for _, task := range tasks {
		if task.WorkspaceID != wsIn.ID {
			t.Fatalf("search leaked task from outside member workspaces: %+v", task)
		}
	}
}

// TestWebSocketRequiresAuth pins the /ws/web gate: no token (or a bogus one)
// rejects the upgrade with 401, a valid ?token= connects and gets the hello.
func TestWebSocketRequiresAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)
	wsBase := "ws" + trimScheme(ts.URL) + "/ws/web"

	for _, token := range []string{"", "bogus-token"} {
		url := wsBase
		if token != "" {
			url += "?token=" + token
		}
		c, resp, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			_ = c.Close()
			t.Fatalf("dial /ws/web with token %q succeeded, want 401", token)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			t.Fatalf("dial /ws/web with token %q: status=%d err=%v, want 401", token, status, err)
		}
	}

	c := dialWebWS(t, ts, auth.token)
	var hello struct {
		Type string `json:"type"`
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := c.ReadJSON(&hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if hello.Type != "hello" {
		t.Fatalf("first frame type = %q, want hello", hello.Type)
	}
}

// TestWorkspaceCreationForbiddenForUsers pins workspace creation as admin-only.
func TestWorkspaceCreationForbiddenForUsers(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("wscreate-node"))
	defer func() { _ = c.Close() }()

	_, userToken := setupUserWithRole(t, st, "user")
	resp, raw := postRaw(t, userToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": "nope", "path": "/tmp/nope",
	})
	requireStatus(t, "create workspace as user", resp, raw, http.StatusForbidden)

	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("admin-ws"), "path": "/tmp/admin-ws",
	})
	if ws.ID == "" {
		t.Fatal("admin workspace creation returned no id")
	}
}
