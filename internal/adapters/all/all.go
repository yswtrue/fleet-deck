// Package all blank-imports every adapter subpackage so that any binary
// importing this package gets every registered adapter in adapters.Default.
// Add new adapter subpackages here as they land.
package all

import (
	_ "fleetdeck/internal/adapters/acp"
	_ "fleetdeck/internal/adapters/fake"
)
