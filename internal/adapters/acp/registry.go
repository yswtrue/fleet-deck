package acp

import "fleetdeck/internal/adapters"

// init registers the shared ACP adapter under every agent type it drives. All
// four share one implementation; only the launch recipe differs (see
// agentInventory in acp.go).
//
// Importing this package for its side effects wires the adapters into the
// process-wide registry:
//
//	import _ "fleetdeck/internal/adapters/acp"
func init() {
	adapters.Default.Register("codex", factory("codex"))
	adapters.Default.Register("claude", factory("claude"))
	adapters.Default.Register("opencode", factory("opencode"))
	adapters.Default.Register("pi", factory("pi"))
}

func factory(agentType string) adapters.Factory {
	return func() adapters.Adapter { return New(agentType) }
}
