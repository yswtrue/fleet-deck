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
)

// dialRegister performs one /ws/node handshake and returns the parsed
// register_response (which carries Error when the hub rejects the claim).
func dialRegister(t *testing.T, ts *httptest.Server, req protocol.RegisterRequest) (*websocket.Conn, protocol.RegisterResponse) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgRegisterRequest, req)); err != nil {
		t.Fatalf("write register_request: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var env protocol.Envelope
	if err := c.ReadJSON(&env); err != nil {
		t.Fatalf("read register_response: %v", err)
	}
	if env.Type != protocol.MsgRegisterResponse {
		t.Fatalf("first reply = %q, want %s", env.Type, protocol.MsgRegisterResponse)
	}
	var resp protocol.RegisterResponse
	if err := env.Decode(&resp); err != nil {
		t.Fatalf("decode register_response: %v", err)
	}
	return c, resp
}

// mintJoinToken uses the HTTP API exactly like the web UI does.
func mintJoinToken(t *testing.T, ts *httptest.Server, bearer string) (string, time.Time) {
	t.Helper()
	resp, raw := postRaw(t, bearer, ts.URL+"/api/nodes/join-tokens", map[string]any{})
	requireStatus(t, "mint join token", resp, raw, http.StatusCreated)
	var body struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode join token: %v", err)
	}
	if body.Token == "" || body.ExpiresAt.IsZero() {
		t.Fatalf("join token response incomplete: %+v", body)
	}
	return body.Token, body.ExpiresAt
}

// TestJoinTokenRegistration pins the onboarding contract: a first-time node
// needs a single-use pairing token, the creator is bound to it, the token is
// spent after one claim, and the issued node token authenticates reconnects.
func TestJoinTokenRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, st, ts, auth := newTestHub(t)
	ctx := context.Background()

	token, expiresAt := mintJoinToken(t, ts, auth.token)
	if until := time.Until(expiresAt); until <= 0 || until > joinTokenTTL+time.Minute {
		t.Fatalf("join token expiry = %v from now, want ~%v", until, joinTokenTTL)
	}

	// No credential: rejected, and the reason reaches the node.
	c, resp := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("jt-none"), MachineID: uniqueName("jt-machine"),
	})
	_ = c.Close()
	if resp.Error == "" || resp.NodeID != "" {
		t.Fatalf("credential-less registration = %+v, want rejection", resp)
	}

	// Valid token: node created, creator bound, node token issued.
	name := uniqueName("jt-node")
	machineID := uniqueName("jt-machine")
	c, resp = dialRegister(t, ts, protocol.RegisterRequest{
		Name: name, Host: "jt-host", JoinToken: token,
		MachineID: machineID,
	})
	if resp.Error != "" || resp.NodeID == "" || resp.Token == "" {
		t.Fatalf("join registration = %+v, want node id + token", resp)
	}
	_ = c.Close()
	bound, err := st.Queries().IsNodeMember(ctx, auth.user.ID, resp.NodeID)
	if err != nil {
		t.Fatalf("check node binding: %v", err)
	}
	if !bound {
		t.Fatal("join token creator was not bound to the new node")
	}

	// The pairing token is single-use.
	c2, resp2 := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("jt-reuse"), JoinToken: token, MachineID: uniqueName("jt-machine"),
	})
	_ = c2.Close()
	if resp2.Error == "" || resp2.NodeID != "" {
		t.Fatalf("reused join token = %+v, want rejection", resp2)
	}

	// The issued node token authenticates the reconnect instead.
	c3, resp3 := dialRegister(t, ts, protocol.RegisterRequest{
		Name: name, Token: resp.Token, MachineID: machineID,
	})
	if resp3.Error != "" || resp3.NodeID != resp.NodeID {
		t.Fatalf("node reconnect = %+v, want node %s", resp3, resp.NodeID)
	}
	_ = c3.Close()

	// Expired tokens are rejected even when unused.
	if _, err := st.Queries().CreateNodeJoinToken(ctx, hashAuthToken("expired-token"), auth.user.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}
	c4, resp4 := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("jt-expired"), JoinToken: "expired-token", MachineID: uniqueName("jt-machine"),
	})
	_ = c4.Close()
	if resp4.Error == "" {
		t.Fatalf("expired join token = %+v, want rejection", resp4)
	}
}

// TestStaticJoinTokenRegistration covers the deployment-level secret used by
// automated/dev stacks: it registers without a database row and without
// binding an owner.
func TestStaticJoinTokenRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	t.Setenv("FLEETDECK_NODE_JOIN_TOKEN", "static-join-secret")
	_, _, ts, _ := newTestHub(t)

	c, resp := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("static-node"), JoinToken: "static-join-secret", MachineID: uniqueName("static-machine"),
	})
	_ = c.Close()
	if resp.Error != "" || resp.NodeID == "" {
		t.Fatalf("static token registration = %+v, want success", resp)
	}
	if resp.Token == "" {
		t.Fatal("static token registration issued no node token")
	}

	c2, resp2 := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("static-wrong"), JoinToken: "wrong-secret", MachineID: uniqueName("static-machine"),
	})
	_ = c2.Close()
	if resp2.Error == "" {
		t.Fatalf("wrong static token = %+v, want rejection", resp2)
	}
}

// TestOneNodePerMachine pins the machine fingerprint rule: one node row per
// computer, regardless of the configured node name, and a node token cannot be
// replayed from a different machine.
func TestOneNodePerMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	t.Setenv("FLEETDECK_NODE_JOIN_TOKEN", "static-join-secret")
	_, _, ts, _ := newTestHub(t)

	machine := uniqueName("machine")
	name := uniqueName("machine-node")
	c, first := dialRegister(t, ts, protocol.RegisterRequest{
		Name: name, JoinToken: "static-join-secret", MachineID: machine,
	})
	_ = c.Close()
	if first.Error != "" || first.NodeID == "" {
		t.Fatalf("first registration = %+v, want success", first)
	}

	// A second identity on the same computer is refused, naming the existing
	// node so the operator can find it.
	c2, second := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("machine-node-2"), JoinToken: "static-join-secret", MachineID: machine,
	})
	_ = c2.Close()
	if !strings.Contains(second.Error, "already registered as") ||
		!strings.Contains(second.Error, name) {
		t.Fatalf("second identity = %+v, want machine-conflict naming %q", second, name)
	}

	// New nodes must state their machine identity.
	c3, missing := dialRegister(t, ts, protocol.RegisterRequest{
		Name: uniqueName("machine-node-3"), JoinToken: "static-join-secret",
	})
	_ = c3.Close()
	if !strings.Contains(missing.Error, "machine_id is required") {
		t.Fatalf("missing machine id = %+v, want machine_id-required error", missing)
	}

	// The issued node token reconnects from the same machine.
	c4, again := dialRegister(t, ts, protocol.RegisterRequest{
		Name: name, Token: first.Token, MachineID: machine,
	})
	_ = c4.Close()
	if again.Error != "" || again.NodeID != first.NodeID {
		t.Fatalf("same-machine reconnect = %+v, want node %s", again, first.NodeID)
	}

	// The same token from another machine is rejected (copied token_file).
	c5, moved := dialRegister(t, ts, protocol.RegisterRequest{
		Name: name, Token: first.Token, MachineID: uniqueName("other-machine"),
	})
	_ = c5.Close()
	if moved.Error == "" {
		t.Fatalf("cross-machine reconnect = %+v, want rejection", moved)
	}
}
