package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		{"POST", "/api/nodes/join-tokens"},
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

// TestNonAdminForbidden pins the management-plane contract: a user-role token
// gets 403 on admin endpoints (user CRUD and node bindings) but can still list
// nodes (its bound set).
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
		{"GET", "/api/users"},
		{"POST", "/api/users"},
		{"PATCH", "/api/users/" + auth.user.ID},
		{"DELETE", "/api/users/" + auth.user.ID},
		{"GET", "/api/users/" + auth.user.ID + "/nodes"},
		{"PUT", "/api/users/" + auth.user.ID + "/nodes"},
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

// ---- user node bindings ----------------------------------------------------

// TestUserNodeBindingsReplace pins PUT /api/users/{id}/nodes: the body is the
// complete binding set (a subset unbinds the rest), the operation is
// idempotent, and both PUT and GET answer with the resulting nodes.
func TestUserNodeBindingsReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, n1 := dialNode(t, ts, st, uniqueName("bind-node1"))
	defer func() { _ = c1.Close() }()
	c2, n2 := dialNode(t, ts, st, uniqueName("bind-node2"))
	defer func() { _ = c2.Close() }()
	c3, n3 := dialNode(t, ts, st, uniqueName("bind-node3"))
	defer func() { _ = c3.Close() }()

	user, _ := setupUserWithRole(t, st, "user")

	assertBound := func(want ...string) {
		t.Helper()
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/users/"+user.ID+"/nodes")
		requireStatus(t, "get user nodes", resp, raw, http.StatusOK)
		var nodes []protocol.Node
		if err := json.Unmarshal(raw, &nodes); err != nil {
			t.Fatalf("decode nodes: %v (%s)", err, raw)
		}
		got := map[string]bool{}
		for _, n := range nodes {
			got[n.ID] = true
		}
		if len(nodes) != len(want) {
			t.Fatalf("bound nodes = %+v, want exactly %v", nodes, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("bound nodes = %+v, want it to include %s", nodes, id)
			}
		}
	}

	// Fresh user: no bindings.
	assertBound()

	// The PUT response carries the resulting set.
	bound := bindUserToNodes(t, ts.URL, auth.token, user.ID, n1, n2)
	if len(bound) != 2 {
		t.Fatalf("PUT response = %+v, want the two bound nodes", bound)
	}
	assertBound(n1, n2)

	// Idempotent: re-PUTting the same set keeps the same result.
	bindUserToNodes(t, ts.URL, auth.token, user.ID, n1, n2)
	assertBound(n1, n2)

	// Full-set semantics: the new set replaces the old one.
	bindUserToNodes(t, ts.URL, auth.token, user.ID, n2, n3)
	assertBound(n2, n3)

	// An empty set unbinds everything.
	bindUserToNodes(t, ts.URL, auth.token, user.ID)
	assertBound()

	// A missing node_ids field is a bad request.
	resp, raw := doRequest(t, http.MethodPut, auth.token, ts.URL+"/api/users/"+user.ID+"/nodes", map[string]any{})
	requireStatus(t, "put without node_ids", resp, raw, http.StatusBadRequest)

	// Unknown users and unknown nodes are 404; a refused replace leaves the
	// binding set untouched.
	resp, raw = doRequest(t, http.MethodPut, auth.token, ts.URL+"/api/users/"+randomUUID(t)+"/nodes",
		map[string]any{"node_ids": []string{}})
	requireStatus(t, "put nodes of unknown user", resp, raw, http.StatusNotFound)
	bindUserToNodes(t, ts.URL, auth.token, user.ID, n1)
	resp, raw = doRequest(t, http.MethodPut, auth.token, ts.URL+"/api/users/"+user.ID+"/nodes",
		map[string]any{"node_ids": []string{randomUUID(t)}})
	requireStatus(t, "put unknown node", resp, raw, http.StatusNotFound)
	assertBound(n1)
}

// TestWorkspaceMemberEndpointsRemoved pins the clean cutover: the
// workspace-member endpoints are gone and every role gets 404 on them.
func TestWorkspaceMemberEndpointsRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("gone-node"))
	defer func() { _ = c.Close() }()
	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("gone-ws"), "path": "/tmp/gone-ws",
	})
	_, userToken := setupUserWithRole(t, st, "user")

	for _, token := range []string{auth.token, userToken} {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/api/workspaces/" + ws.ID + "/members"},
			{http.MethodPost, "/api/workspaces/" + ws.ID + "/members"},
			{http.MethodDelete, "/api/workspaces/" + ws.ID + "/members/" + randomUUID(t)},
		} {
			resp, raw := doRequest(t, tc.method, token, ts.URL+tc.path, map[string]string{"user_id": "x"})
			requireStatus(t, tc.method+" "+tc.path, resp, raw, http.StatusNotFound)
		}
	}
}

// ---- node binding scoping --------------------------------------------------

// bindUserToNodes replaces a user's full node binding set through the admin
// API and returns the resulting bound nodes.
func bindUserToNodes(t *testing.T, tsURL, adminToken, userID string, nodeIDs ...string) []protocol.Node {
	t.Helper()
	ids := nodeIDs
	if ids == nil {
		ids = []string{}
	}
	resp, raw := doRequest(t, http.MethodPut, adminToken, tsURL+"/api/users/"+userID+"/nodes",
		map[string]any{"node_ids": ids})
	return decodeOK[[]protocol.Node](t, "PUT /api/users/{id}/nodes", resp, raw)
}

// bindingFixture provisions one node holding a workspace and a dispatched
// task (with its session), plus a plain user with a bearer token. The user
// starts unbound; bind it via bindUserToNodes or a direct store insert.
type bindingFixture struct {
	nodeID    string
	workspace protocol.Workspace
	task      protocol.Task
	user      store.User
	userToken string
}

func newBindingFixture(t *testing.T, st *store.Store, ts *httptest.Server, adminToken string) bindingFixture {
	t.Helper()
	c, nodeID := dialNode(t, ts, st, uniqueName("bind-node"))
	t.Cleanup(func() { _ = c.Close() })

	ws := postJSON[protocol.Workspace](t, adminToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("bind-ws"), "path": "/tmp/bind-ws",
	})
	task := postJSON[protocol.Task](t, adminToken, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws.ID, "title": uniqueName("bind-task"), "prompt": "p", "agent_type": "fake",
	})
	user, userToken := setupUserWithRole(t, st, "user")
	return bindingFixture{nodeID: nodeID, workspace: ws, task: task, user: user, userToken: userToken}
}

// TestNodeVisibilityForNonAdmins pins node discovery scoping: a non-admin's
// node list holds only bound nodes and an unbound node id is 404 (no
// existence leak). Binding flips exactly that node visible.
func TestNodeVisibilityForNonAdmins(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, nodeID1 := dialNode(t, ts, st, uniqueName("vis-node1"))
	defer func() { _ = c1.Close() }()
	c2, nodeID2 := dialNode(t, ts, st, uniqueName("vis-node2"))
	defer func() { _ = c2.Close() }()

	user, userToken := setupUserWithRole(t, st, "user")

	listNodes := func() []protocol.Node {
		t.Helper()
		resp, raw := getRaw(t, userToken, ts.URL+"/api/nodes")
		requireStatus(t, "user list nodes", resp, raw, http.StatusOK)
		var nodes []protocol.Node
		if err := json.Unmarshal(raw, &nodes); err != nil {
			t.Fatalf("decode nodes: %v (%s)", err, raw)
		}
		return nodes
	}

	// Unbound: an empty list and 404 on both ids.
	if nodes := listNodes(); len(nodes) != 0 {
		t.Fatalf("unbound user sees %d nodes, want 0: %+v", len(nodes), nodes)
	}
	for _, id := range []string{nodeID1, nodeID2} {
		resp, raw := getRaw(t, userToken, ts.URL+"/api/nodes/"+id)
		requireStatus(t, "get unbound node "+id, resp, raw, http.StatusNotFound)
	}

	// Binding node1 flips exactly node1 visible.
	bound := bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeID1)
	if len(bound) != 1 || bound[0].ID != nodeID1 {
		t.Fatalf("bound nodes = %+v, want exactly [%s]", bound, nodeID1)
	}
	nodes := listNodes()
	if len(nodes) != 1 || nodes[0].ID != nodeID1 {
		t.Fatalf("nodes after binding = %+v, want exactly [%s]", nodes, nodeID1)
	}
	resp, raw := getRaw(t, userToken, ts.URL+"/api/nodes/"+nodeID1)
	requireStatus(t, "get bound node", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/nodes/"+nodeID2)
	requireStatus(t, "get unbound node after binding", resp, raw, http.StatusNotFound)
}

// TestWorkspaceScopingForUnboundUsers pins the subtree visibility rules: an
// unbound user reads 404 across the node's subtree (workspace, tasks,
// subtasks, sessions, messages, events, send) and gets 403 on writes
// (workspace/task creation); PATCH of an invisible task is 404. Binding the
// user to the node flips the whole subtree visible and writable; unbinding
// flips reads back to 404.
func TestWorkspaceScopingForUnboundUsers(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	f := newBindingFixture(t, st, ts, auth.token)
	if f.task.SessionID == "" {
		t.Fatalf("task has no session: %+v", f.task)
	}

	// Unbound reads: everything in the node's subtree is 404.
	for _, path := range []string{
		"/api/workspaces/" + f.workspace.ID,
		"/api/tasks?workspace_id=" + f.workspace.ID,
		"/api/tasks?node_id=" + f.nodeID,
		"/api/tasks/" + f.task.ID + "/subtasks",
		"/api/sessions/" + f.task.SessionID,
		"/api/sessions/" + f.task.SessionID + "/messages",
		"/api/sessions/" + f.task.SessionID + "/events",
	} {
		resp, raw := getRaw(t, f.userToken, ts.URL+path)
		requireStatus(t, "GET "+path, resp, raw, http.StatusNotFound)
	}
	resp, raw := postRaw(t, f.userToken, ts.URL+"/api/sessions/"+f.task.SessionID+"/send",
		map[string]string{"message": "hi"})
	requireStatus(t, "POST send as unbound", resp, raw, http.StatusNotFound)

	// Unbound writes: 403 on creation, 404 on patching an invisible task.
	resp, raw = postRaw(t, f.userToken, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": f.workspace.ID, "title": "nope",
	})
	requireStatus(t, "create task as unbound", resp, raw, http.StatusForbidden)
	resp, raw = patchRaw(t, f.userToken, ts.URL+"/api/tasks/"+f.task.ID, map[string]any{"sort_order": 3})
	requireStatus(t, "patch task as unbound", resp, raw, http.StatusNotFound)
	resp, raw = postRaw(t, f.userToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": f.nodeID, "name": "nope", "path": "/tmp/nope",
	})
	requireStatus(t, "create workspace as unbound", resp, raw, http.StatusForbidden)

	// The unbound node is absent from discovery, and its workspace list is
	// 404 -- never a leaky empty list.
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/nodes")
	requireStatus(t, "user list nodes", resp, raw, http.StatusOK)
	var nodes []protocol.Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		t.Fatalf("decode nodes: %v (%s)", err, raw)
	}
	for _, n := range nodes {
		if n.ID == f.nodeID {
			t.Fatalf("unbound user sees node %q in list", f.nodeID)
		}
	}
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/workspaces?node_id="+f.nodeID)
	requireStatus(t, "user list workspaces on unbound node", resp, raw, http.StatusNotFound)

	// Binding the user flips the whole subtree visible and writable.
	bindUserToNodes(t, ts.URL, auth.token, f.user.ID, f.nodeID)

	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/workspaces/"+f.workspace.ID)
	requireStatus(t, "bound get workspace", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/tasks?workspace_id="+f.workspace.ID)
	requireStatus(t, "bound list tasks", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/tasks?node_id="+f.nodeID)
	requireStatus(t, "bound list tasks by node", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/tasks/"+f.task.ID+"/subtasks")
	requireStatus(t, "bound list subtasks", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/sessions/"+f.task.SessionID)
	requireStatus(t, "bound get session", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/sessions/"+f.task.SessionID+"/messages")
	requireStatus(t, "bound list messages", resp, raw, http.StatusOK)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/sessions/"+f.task.SessionID+"/events")
	requireStatus(t, "bound list events", resp, raw, http.StatusOK)

	created := postJSON[protocol.Task](t, f.userToken, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": f.workspace.ID, "title": uniqueName("bound-task"), "prompt": "p", "agent_type": "fake",
	})
	if created.ID == "" {
		t.Fatal("bound task creation returned no id")
	}
	createdWS := postJSON[protocol.Workspace](t, f.userToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": f.nodeID, "name": uniqueName("bound-ws"), "path": "/tmp/bound-ws",
	})
	if createdWS.ID == "" {
		t.Fatal("bound workspace creation returned no id")
	}

	// Unbinding flips reads back to 404.
	bindUserToNodes(t, ts.URL, auth.token, f.user.ID)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/tasks?workspace_id="+f.workspace.ID)
	requireStatus(t, "list tasks after unbind", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, f.userToken, ts.URL+"/api/sessions/"+f.task.SessionID)
	requireStatus(t, "get session after unbind", resp, raw, http.StatusNotFound)
}

// TestTasksByNodeScopedToBoundNodes pins node-scoped task lists and fuzzy
// search: a non-admin sees every task beneath a bound node (across all of its
// workspaces) and gets 404 for an unbound node -- for plain lists and q=
// searches alike.
func TestTasksByNodeScopedToBoundNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("filternode"))
	defer func() { _ = c.Close() }()
	c2, otherNodeID := dialNode(t, ts, st, uniqueName("othernode"))
	defer func() { _ = c2.Close() }()

	mkWS := func(node, name string) protocol.Workspace {
		return postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
			"node_id": node, "name": name, "path": "/tmp/" + name,
		})
	}
	mkTask := func(wsID, title string) protocol.Task {
		return postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
			"workspace_id": wsID, "title": title, "prompt": "p", "agent_type": "fake",
		})
	}
	wsIn := mkWS(nodeID, uniqueName("filter-in"))
	wsIn2 := mkWS(nodeID, uniqueName("filter-in2"))
	wsOut := mkWS(otherNodeID, uniqueName("filter-out"))
	inTask := mkTask(wsIn.ID, uniqueName("in-task"))
	inTask2 := mkTask(wsIn2.ID, uniqueName("in-task2"))
	_ = mkTask(wsOut.ID, uniqueName("out-task"))

	user, userToken := setupUserWithRole(t, st, "user")
	bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeID)

	// Every task beneath the bound node is listed, across its workspaces.
	resp, raw := getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+nodeID)
	requireStatus(t, "filtered list by node", resp, raw, http.StatusOK)
	var tasks []protocol.Task
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("decode tasks: %v (%s)", err, raw)
	}
	got := map[string]bool{}
	for _, task := range tasks {
		got[task.ID] = true
	}
	if len(tasks) != 2 || !got[inTask.ID] || !got[inTask2.ID] {
		t.Fatalf("tasks by bound node = %v, want exactly [%s %s]", tasks, inTask.ID, inTask2.ID)
	}

	// q= search is scoped to bound workspaces the same way.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+nodeID+"&q="+inTask.Title)
	requireStatus(t, "filtered search by node", resp, raw, http.StatusOK)
	tasks = nil
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("decode tasks: %v (%s)", err, raw)
	}
	sawWanted := false
	for _, task := range tasks {
		if task.WorkspaceID != wsIn.ID && task.WorkspaceID != wsIn2.ID {
			t.Fatalf("search leaked task from outside bound workspaces: %+v", task)
		}
		if task.ID == inTask.ID {
			sawWanted = true
		}
	}
	if !sawWanted {
		t.Fatalf("search for %q returned %+v, want it to include %s", inTask.Title, tasks, inTask.ID)
	}

	// An unbound node is 404 for lists and searches alike.
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+otherNodeID)
	requireStatus(t, "list tasks by unbound node", resp, raw, http.StatusNotFound)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/tasks?node_id="+otherNodeID+"&q=whatever")
	requireStatus(t, "search tasks by unbound node", resp, raw, http.StatusNotFound)
}

// TestWorkspaceCreationFollowsBinding pins POST /api/workspaces under the
// binding model: unbound users get 403 on a node, bound users may create
// workspaces there (subtree inheritance), and admins create anywhere.
func TestWorkspaceCreationFollowsBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("wscreate-node"))
	defer func() { _ = c.Close() }()

	user, userToken := setupUserWithRole(t, st, "user")
	resp, raw := postRaw(t, userToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": "nope", "path": "/tmp/nope",
	})
	requireStatus(t, "create workspace as unbound user", resp, raw, http.StatusForbidden)

	// Binding the user grants workspace creation on that node.
	bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeID)
	ws := postJSON[protocol.Workspace](t, userToken, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("bound-ws"), "path": "/tmp/bound-ws",
	})
	if ws.ID == "" {
		t.Fatal("bound workspace creation returned no id")
	}

	// Admins keep creating anywhere.
	adminWS := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("admin-ws"), "path": "/tmp/admin-ws",
	})
	if adminWS.ID == "" {
		t.Fatal("admin workspace creation returned no id")
	}
}

// ---- web socket scoping ----------------------------------------------------

// helloFrame is the first /ws/web frame: a hello carrying the node list
// visible to the connecting user.
type helloFrame struct {
	Type string `json:"type"`
	Data struct {
		Nodes []protocol.Node `json:"nodes"`
	} `json:"data"`
}

// readHello reads the hello frame off a freshly dialed /ws/web connection.
func readHello(t *testing.T, c *websocket.Conn) helloFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var hello helloFrame
	if err := c.ReadJSON(&hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if hello.Type != "hello" {
		t.Fatalf("first frame type = %q, want hello", hello.Type)
	}
	return hello
}

// expectSilent asserts that a /ws/web connection receives no event at all
// within a short window.
func expectSilent(t *testing.T, ch <-chan protocol.Envelope) {
	t.Helper()
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("connection closed while asserting no events are delivered")
			}
			t.Fatalf("event leaked to a scoped-out subscriber: %+v", env)
		case <-timeout:
			return
		}
	}
}

// TestWebWSHelloScopedToBoundNodes pins the /ws/web hello frame: it carries
// every node for admins and only the bound nodes for anyone else (empty for
// an unbound user) -- the same view GET /api/nodes reports.
func TestWebWSHelloScopedToBoundNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c1, nodeID1 := dialNode(t, ts, st, uniqueName("hello-node1"))
	defer func() { _ = c1.Close() }()
	c2, nodeID2 := dialNode(t, ts, st, uniqueName("hello-node2"))
	defer func() { _ = c2.Close() }()

	user, userToken := setupUserWithRole(t, st, "user")

	// Admin: the hello carries both nodes (possibly among leftover rows).
	adminHello := readHello(t, dialWebWS(t, ts, auth.token))
	got := map[string]bool{}
	for _, n := range adminHello.Data.Nodes {
		got[n.ID] = true
	}
	if !got[nodeID1] || !got[nodeID2] {
		t.Fatalf("admin hello nodes = %+v, want it to include %s and %s", adminHello.Data.Nodes, nodeID1, nodeID2)
	}

	// Unbound user: an empty node list.
	unboundHello := readHello(t, dialWebWS(t, ts, userToken))
	if len(unboundHello.Data.Nodes) != 0 {
		t.Fatalf("unbound hello nodes = %+v, want none", unboundHello.Data.Nodes)
	}

	// Bound to node1: exactly that node.
	bindUserToNodes(t, ts.URL, auth.token, user.ID, nodeID1)
	boundHello := readHello(t, dialWebWS(t, ts, userToken))
	if len(boundHello.Data.Nodes) != 1 || boundHello.Data.Nodes[0].ID != nodeID1 {
		t.Fatalf("bound hello nodes = %+v, want exactly [%s]", boundHello.Data.Nodes, nodeID1)
	}
}

// TestWebWSBroadcastScopedByNodeBinding pins per-connection broadcast
// scoping: an event inside one user's subtree reaches that user's socket and
// the admin's, but never a differently bound peer's.
func TestWebWSBroadcastScopedByNodeBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	cA, nodeA := dialNode(t, ts, st, uniqueName("scope-nodea"))
	defer func() { _ = cA.Close() }()
	cB, nodeB := dialNode(t, ts, st, uniqueName("scope-nodeb"))
	defer func() { _ = cB.Close() }()

	wsA := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeA, "name": uniqueName("scope-ws-a"), "path": "/tmp/scope-ws-a",
	})
	taskA := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": wsA.ID, "title": uniqueName("scope-task-a"), "prompt": "p", "agent_type": "fake",
	})
	if taskA.SessionID == "" {
		t.Fatalf("task has no session: %+v", taskA)
	}

	userA, tokenA := setupUserWithRole(t, st, "user")
	userB, tokenB := setupUserWithRole(t, st, "user")
	bindUserToNodes(t, ts.URL, auth.token, userA.ID, nodeA)
	bindUserToNodes(t, ts.URL, auth.token, userB.ID, nodeB)

	adminEvents := readEnvelopes(dialWebWS(t, ts, auth.token))
	aEvents := readEnvelopes(dialWebWS(t, ts, tokenA))
	bEvents := readEnvelopes(dialWebWS(t, ts, tokenB))
	for _, ch := range []<-chan protocol.Envelope{adminEvents, aEvents, bEvents} {
		waitEnvelope(t, ch, "hello")
	}

	// A real mutation in A's subtree: task creation publishes task.created
	// plus the dispatch state changes, all scoped to nodeA.
	created := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": wsA.ID, "title": uniqueName("scope-task-a2"), "prompt": "p", "agent_type": "fake",
	})
	got := waitEnvelope(t, aEvents, protocol.EvTaskCreated)
	var tc protocol.TaskCreated
	if err := got.Decode(&tc); err != nil || tc.Task.ID != created.ID {
		t.Fatalf("A got task.created %+v (err %v), want task %s", tc.Task, err, created.ID)
	}
	waitEnvelope(t, adminEvents, protocol.EvTaskCreated)
	// The instruction is buffered as the first transcript line, for A and the
	// admin, never for B.
	promptEnv := waitEnvelope(t, aEvents, protocol.EvSessionMessage)
	var pm protocol.SessionMessage
	if err := promptEnv.Decode(&pm); err != nil || pm.Message.Content != "p" {
		t.Fatalf("A got session.message %+v (err %v), want the dispatch prompt", pm.Message, err)
	}
	waitEnvelope(t, adminEvents, protocol.EvSessionMessage)
	expectSilent(t, bEvents)

	// A session event in the same subtree (arriving from the node) is scoped
	// the same way.
	msg := protocol.SessionMessage{
		SessionID: taskA.SessionID,
		Message: protocol.Message{
			ID: "m1", SessionID: taskA.SessionID,
			Role: protocol.RoleAssistant, Content: uniqueName("scoped-msg"),
			CreatedAt: time.Now().UTC(),
		},
	}
	if err := cA.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionEvent, msg)); err != nil {
		t.Fatalf("send session.event: %v", err)
	}
	got = waitEnvelope(t, aEvents, protocol.EvSessionMessage)
	var sm protocol.SessionMessage
	if err := got.Decode(&sm); err != nil || sm.Message.Content != msg.Message.Content {
		t.Fatalf("A got session.message %+v (err %v), want content %q", sm.Message, err, msg.Message.Content)
	}
	waitEnvelope(t, adminEvents, protocol.EvSessionMessage)
	expectSilent(t, bEvents)
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
