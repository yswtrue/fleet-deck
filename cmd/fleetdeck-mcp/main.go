// Command fleetdeck-mcp serves FleetDeck state to coding agents over the Model
// Context Protocol using stdio (newline-delimited JSON-RPC on stdin/stdout).
//
// It is launched by the agent (or an ACP adapter) as a subprocess; it is not a
// network daemon. All reads go through the hub's REST API.
//
// Configuration:
//
//	FLEETDECK_HUB_URL    hub REST base URL (default "http://127.0.0.1:7710")
//	FLEETDECK_HUB_TOKEN  bearer token for the hub's protected REST API.
//	                     When unset, no Authorization header is sent and the
//	                     hub answers 401 on every protected endpoint.
//
// stdout carries MCP frames, so all diagnostics are written to stderr.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	fdmcp "fleetdeck/internal/mcp"
)

func main() {
	// The SDK's stdio transport owns stdout; keep logs on stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("fleetdeck-mcp exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := fdmcp.NewHubClientWithToken(getenv("FLEETDECK_HUB_URL", fdmcp.DefaultHubURL), os.Getenv("FLEETDECK_HUB_TOKEN"))
	srv := fdmcp.NewServer(hub)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		// A SIGINT/SIGTERM-driven shutdown cancels ctx; that is a clean exit.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
