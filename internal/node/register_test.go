package node

import (
	"slices"
	"testing"

	"fleetdeck/internal/adapters"
)

// TestLiveSessionIDsSnapshot pins the handshake list the hub reconciles
// against: sorted for a stable wire payload, and never nil (nil means a daemon
// that predates the field, which the hub must not reconcile).
func TestLiveSessionIDsSnapshot(t *testing.T) {
	n := &Node{sessions: map[string]adapters.Adapter{"b": nil, "a": nil}}
	got := n.liveSessionIDs()
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("liveSessionIDs = %v, want [a b]", got)
	}

	if empty := (&Node{sessions: map[string]adapters.Adapter{}}).liveSessionIDs(); empty == nil {
		t.Fatal("liveSessionIDs on an idle daemon = nil, want an empty array")
	}
}
