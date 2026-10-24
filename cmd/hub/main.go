// Command hub is the FleetDeck control plane: HTTP API, web subscriber socket,
// node command/event socket, and the only writer to Postgres.
//
// Configuration is environment-only (no baked-in addresses or credentials;
// local dev gets these from .env via the devenv shell):
//
//	FLEETDECK_HUB_ADDR      HTTP + WebSocket listen address (required, e.g. "127.0.0.1:7710")
//	FLEETDECK_DATABASE_URL  Postgres DSN (required)
//	FLEETDECK_REDIS_URL     redis:// URL for the read-through cache
//	                        (unset = no cache; a down Redis degrades to DB reads)
//	FLEETDECK_TASK_RETRY_LIMIT  automatic re-dispatches of a task whose node
//	                            restarted (default 1, 0 disables)
//
// Nodes connect to /ws/node and web clients to /ws/web.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
	"fleetdeck/internal/hub"
	"fleetdeck/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("hub exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("FLEETDECK_DATABASE_URL")
	if dsn == "" {
		return errors.New("FLEETDECK_DATABASE_URL is required (inside devenv shell it is derived from .env)")
	}
	st, err := store.New(ctx, dsn)
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
		// Surface a misconfigured/down Redis immediately: the cache still
		// degrades to Postgres, but every miss then costs a probe instead of
		// a cache round trip.
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if err := rc.Ping(pingCtx); err != nil {
			slog.Warn("redis unreachable; cache operations will fall back to Postgres", "url", url, "err", err)
		}
		cancel()
	} else {
		slog.Info("redis cache disabled (FLEETDECK_REDIS_URL unset)")
	}

	h := hub.New(ctx, st, adapters.Default, c)
	defer func() { _ = h.Close() }()

	return h.Run(ctx)
}
