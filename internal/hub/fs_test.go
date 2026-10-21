package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"fleetdeck/internal/protocol"
)

// TestNodeDirListingProxy drives GET /api/nodes/{id}/fs end to end: the hub
// forwards fs.list_req to the connected node and returns the node's listing
// without leaking the internal request id.
func TestNodeDirListingProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-fs-node"))
	defer func() { _ = c.Close() }()

	requests := make(chan protocol.FSListRequest, 1)
	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgFSList {
				continue
			}
			var req protocol.FSListRequest
			if err := env.Decode(&req); err != nil {
				return
			}
			requests <- req
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgFSListResp, protocol.FSListResponse{
				ReqID:  req.ReqID,
				Path:   "/srv/code",
				Parent: "/srv",
				Entries: []protocol.FSDirEntry{
					{Name: "app", Path: "/srv/code/app"},
				},
			}))
		}
	}()

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+nodeID+"/fs?path=/srv/code")
	requireStatus(t, "node fs", resp, raw, http.StatusOK)
	var listing dirListing
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatalf("decode listing: %v (%s)", err, raw)
	}
	if listing.Path != "/srv/code" || listing.Parent != "/srv" {
		t.Fatalf("listing = %+v, want path /srv/code parent /srv", listing)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "app" {
		t.Fatalf("entries = %+v, want [app]", listing.Entries)
	}
	if bytes.Contains(raw, []byte(`"req_id"`)) {
		t.Fatalf("web response leaks the internal req_id: %s", raw)
	}

	select {
	case req := <-requests:
		if req.Path != "/srv/code" {
			t.Fatalf("node saw path %q, want /srv/code", req.Path)
		}
	default:
		t.Fatal("node never received fs.list_req")
	}
}

// TestNodeDirListingOffline covers the offline node: no WS connection means
// the hub cannot list anything and must say so instead of blocking.
func TestNodeDirListingOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	node := seedNode(t, st, uniqueName("hub-fs-offline"))

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+node.ID+"/fs")
	requireStatus(t, "offline node fs", resp, raw, http.StatusServiceUnavailable)
	if got := decodeErr(t, raw); got != "node is offline" {
		t.Fatalf("error = %q, want %q", got, "node is offline")
	}
}
