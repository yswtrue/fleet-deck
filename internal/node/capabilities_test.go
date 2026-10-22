package node

import (
	"reflect"
	"testing"
)

// Capability labels are free-form; adapter keys travel in AgentTypes and must
// not be duplicated into the label list.
func TestAdvertisedCapabilitiesAreLabelsOnly(t *testing.T) {
	cfg := Config{
		AgentTypes:   []string{"omp", "codex"},
		Capabilities: []string{"gpu", "", "gpu", "nixos"},
	}
	got := cfg.advertisedCapabilities()
	if want := []string{"gpu", "nixos"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("advertisedCapabilities = %v, want %v", got, want)
	}

	empty := (&Config{AgentTypes: []string{"omp"}}).advertisedCapabilities()
	if empty == nil {
		t.Fatal("advertisedCapabilities returned nil; an explicit empty array clears stale labels")
	}
	if len(empty) != 0 {
		t.Fatalf("advertisedCapabilities = %v, want empty", empty)
	}
}
