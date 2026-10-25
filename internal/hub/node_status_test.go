package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// TestNodeHeartbeatAdvancesLastSeen pins the UI rule: a node's last_seen_at is
// its latest WebSocket heartbeat (the pong answering the hub's ping), not the
// time it established the connection. Without this, a node that stays connected
// for days would keep showing its handshake time.
func TestNodeHeartbeatAdvancesLastSeen(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	// Shorten the production 30 s heartbeat so the test observes a real
	// ping -> gorilla auto-pong -> last_seen_at update sequence.
	h.nodePingInterval = 20 * time.Millisecond

	c, nodeID := dialNode(t, ts, st, uniqueName("hub-heartbeat-node"))
	t.Cleanup(func() { _ = c.Close() })

	// Gorilla answers pings with pongs only while the read side is running.
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	getNode := func() protocol.Node {
		t.Helper()
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+nodeID)
		requireStatus(t, "get heartbeat node", resp, raw, http.StatusOK)
		var node protocol.Node
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("decode heartbeat node: %v (%s)", err, raw)
		}
		return node
	}

	initial := getNode()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if heartbeat := getNode(); heartbeat.LastSeenAt.After(initial.LastSeenAt) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("last_seen_at did not advance past the registration time %s",
				initial.LastSeenAt)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNodeOfflineKeepsLastHeartbeat pins the other half of the same rule: the
// offline cascade runs after the grace period and must not overwrite the final
// heartbeat with the later disconnect-detection time.
func TestNodeOfflineKeepsLastHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, _ := newTestHub(t)
	// Disable periodic pings so the only heartbeat is the explicit pong below.
	h.nodePingInterval = time.Hour

	c, nodeID := dialNode(t, ts, st, uniqueName("hub-offline-heartbeat-node"))
	t.Cleanup(func() { _ = c.Close() })
	initial, err := st.Queries().GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("get node row: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := c.WriteMessage(websocket.PongMessage, nil); err != nil {
		t.Fatalf("send heartbeat pong: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var heartbeat time.Time
	for {
		row, err := st.Queries().GetNode(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get node row: %v", err)
		}
		if row.LastSeenAt.After(initial.LastSeenAt) {
			heartbeat = row.LastSeenAt
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last_seen_at %s did not advance past registration time %s",
				row.LastSeenAt, initial.LastSeenAt)
		}
		time.Sleep(10 * time.Millisecond)
	}

	_ = c.Close()

	deadline = time.Now().Add(5 * time.Second)
	for {
		row, err := st.Queries().GetNode(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get node row: %v", err)
		}
		if row.Status == protocol.NodeOffline {
			if !row.LastSeenAt.Equal(heartbeat) {
				t.Fatalf("offline last_seen_at = %s, want final heartbeat %s",
					row.LastSeenAt, heartbeat)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node row still %q after disconnect", row.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNodeStatusReflectsLiveConnection pins the rule the UI depends on: node
// status is connectivity, so a connected node reads online and the same row
// reads offline once its socket goes away.
func TestNodeStatusReflectsLiveConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-status-node"))

	var node protocol.Node
	// Registration adds the connection to the registry just after replying,
	// so poll until the hub reports the node online.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+nodeID)
		requireStatus(t, "get live node", resp, raw, http.StatusOK)
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("decode node: %v (%s)", err, raw)
		}
		if node.Status == protocol.NodeOnline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connected node status = %q, want online", node.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = c.Close()

	// The hub reflects the disconnect asynchronously; poll the API.
	deadline = time.Now().Add(5 * time.Second)
	for {
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+nodeID)
		requireStatus(t, "get disconnected node", resp, raw, http.StatusOK)
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("decode node: %v (%s)", err, raw)
		}
		if node.Status == protocol.NodeOffline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected node still reports %q, want offline", node.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStaleOnlineRowIsNotReportedOnline reproduces the reason a node can look
// online with no daemon running: a hub process that died while nodes were
// connected never received their disconnect frames, leaving the DB rows
// marked online. A running hub with no such connection must report offline,
// and a fresh hub must reset the persisted row.
func TestStaleOnlineRowIsNotReportedOnline(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-stale-node"))
	ctx := context.Background()
	if err := st.Queries().UpdateNodeStatus(ctx, node.ID, protocol.NodeOnline, time.Now().UTC()); err != nil {
		t.Fatalf("seed online row: %v", err)
	}

	// The row says online but this hub holds no connection: the API must not
	// parrot the stale column.
	resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get stale node", resp, raw, http.StatusOK)
	var got protocol.Node
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node: %v (%s)", err, raw)
	}
	if got.Status != protocol.NodeOffline {
		t.Fatalf("stale row reported %q, want offline", got.Status)
	}

	resp, raw = getRaw(t, auth.token, ts.URL+"/api/nodes")
	requireStatus(t, "list stale nodes", resp, raw, http.StatusOK)
	var list []protocol.Node
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode nodes: %v (%s)", err, raw)
	}
	for _, n := range list {
		if n.ID == node.ID && n.Status != protocol.NodeOffline {
			t.Fatalf("list reported stale node as %q, want offline", n.Status)
		}
	}

	// A restart (fresh Hub on the same store) resets the persisted column.
	stale := New(ctx, st, adapters.NewRegistry(), nil)
	t.Cleanup(func() { _ = stale.Close() })
	row, err := st.Queries().GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node after restart: %v", err)
	}
	if row.Status != protocol.NodeOffline {
		t.Fatalf("status after hub restart = %q, want offline", row.Status)
	}
}
