// Command hub is the FleetDeck control plane: HTTP API, web subscriber socket,
// node command/event socket, and the only writer to Postgres.
//
// Configuration is environment-only:
//
//	FLEETDECK_HUB_ADDR      HTTP + WebSocket listen address (default ":7710")
//	FLEETDECK_DATABASE_URL  Postgres DSN
//	                        (default "postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck?sslmode=disable")
//
// Nodes connect to /ws/node and web clients to /ws/web.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/hub"
	"fleetdeck/internal/store"
)

const defaultDSN = "postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck?sslmode=disable"

func main() {
	if err := run(); err != nil {
		slog.Error("hub exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, getenv("FLEETDECK_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	h := hub.New(ctx, st, adapters.Default)
	defer func() { _ = h.Close() }()

	return h.Run(ctx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
