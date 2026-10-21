package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

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
