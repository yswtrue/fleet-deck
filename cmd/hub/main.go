// Command hub is the FleetDeck control plane: HTTP API, web subscriber socket,
// node command/event socket, and the only writer to Postgres.
//
// Configuration is environment-only:
//
//	FLEETDECK_HUB_ADDR      HTTP + WebSocket listen address (default ":7710")
//	FLEETDECK_DATABASE_URL  Postgres DSN
//	                        (default "postgres://fleetdeck:fleetdeck@127.0.0.1:55432/fleetdeck?sslmode=disable")
//	FLEETDECK_REDIS_URL     redis:// URL for the read-through cache
//	                        (unset = no cache; a down Redis degrades to DB reads)
//
// Nodes connect to /ws/node and web clients to /ws/web.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
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

	// Redis-backed read-through cache (auth tokens, task lists). Unset or
	// unreachable Redis degrades to direct database reads; requests never
	// fail because of the cache.
	var c cache.Cache = cache.Noop{}
	if url := os.Getenv("FLEETDECK_REDIS_URL"); url != "" {
		rc, err := cache.NewRedis(url)
		if err != nil {
			return fmt.Errorf("redis url: %w", err)
		}
		c = rc
		slog.Info("redis cache enabled", "url", url)
	} else {
		slog.Info("redis cache disabled (FLEETDECK_REDIS_URL unset)")
	}

	h := hub.New(ctx, st, adapters.Default, c)
	defer func() { _ = h.Close() }()

	return h.Run(ctx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
