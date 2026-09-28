package hub

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"
)

// Handler returns the hub's HTTP + WebSocket handler tree.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	h.routes(mux)
	return withCORS(mux)
}

// withCORS admits the web UI, which is served from a different localhost port
// (vite at :7711 -> hub at :7710). The hub is a single-user dev tool, so the
// policy matches the WebSocket upgrader's accept-any-origin. Preflight is
// answered here because the web API client sends Content-Type on every request,
// which browsers must preflight.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run serves the hub on FLEETDECK_HUB_ADDR (default ":7710") and blocks until
// ctx is cancelled, then shuts down gracefully within a bounded window.
func (h *Hub) Run(ctx context.Context) error {
	addr := os.Getenv("FLEETDECK_HUB_ADDR")
	if addr == "" {
		addr = ":7710"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           h.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		h.log.Info("hub listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		h.log.Info("hub shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
