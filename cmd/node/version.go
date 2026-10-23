package main

import (
	"fmt"
	"runtime"
)

// version is the release tag stamped by the release workflow
// (-ldflags "-X main.version=v1.2.3"). Source builds report "dev".
var version = "dev"

// runVersion implements `node version`. `update` compares this string with the
// latest release tag, so a build without the ldflag cannot claim to be current.
func runVersion() error {
	fmt.Printf("fleetdeck-node %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	return nil
}
