// Package version holds the release version stamped into every FleetDeck
// binary at build time.
package version

// Version is the release tag stamped by the release workflow
// (-ldflags "-X fleetdeck/internal/version.Version=v1.2.3"). Source builds
// report "dev".
var Version = "dev"
