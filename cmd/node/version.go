package main

import (
	"fmt"
	"runtime"

	"fleetdeck/internal/version"
)

// runVersion implements `node version`. `update` compares this string with the
// latest release tag, so a build without the ldflag cannot claim to be current.
func runVersion() error {
	fmt.Printf("fleetdeck-node %s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
	return nil
}
