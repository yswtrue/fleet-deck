package node

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
)

// TestReconnectDelayResetsAfterRegistration pins the backoff contract: after a
// few outages the wait sits at the 30s ceiling, and a successful registration
// must drop it back to 1s. Without the reset (the old behavior) a one-second
// blip after an earlier long outage waited the full 30s, racing the hub's own
// 30s offline grace.
func TestReconnectDelayResetsAfterRegistration(t *testing.T) {
	var r reconnectDelay
	for i, want := range []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	} {
		if got := r.next(); got != want {
			t.Fatalf("attempt %d wait = %s, want %s", i+1, got, want)
		}
	}

	r.reset()
	if got := r.next(); got != time.Second {
		t.Fatalf("wait after reset = %s, want %s", got, time.Second)
	}
}

// TestNodeReconnectsAfterSilentLinkLoss pins the node-side liveness bound. A
// link that dies without FIN/RST (NAT rebinding, one-way packet loss) delivers
// neither reads nor write errors, so the read loop used to block forever and
// the daemon never re-entered Run's reconnect loop. The scripted hub registers
// each connection and then stays silent; only the node's read deadline can
// notice, and it must register again.
func TestNodeReconnectsAfterSilentLinkLoss(t *testing.T) {
	// A dev shell may export FLEETDECK_EVENT_BUFFER_DIR; replaying a stale
	// on-disk queue into this scripted hub would inject foreign events.
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")

	var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	hold := make(chan struct{})
	regs := make(chan struct{}, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var reg protocol.Envelope
		if err := c.ReadJSON(&reg); err != nil || reg.Type != protocol.MsgRegisterRequest {
			return
		}
		resp := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "silent-link"})
		if err := c.WriteJSON(resp); err != nil {
			return
		}
		select {
		case regs <- struct{}{}:
		default:
		}
		<-hold // never ping, never send: only the node's deadline can react
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer close(hold)

	cfg := Config{
		Name:           "silent-node",
		Host:           "test",
		AgentTypes:     []string{"fake"},
		DefaultAdapter: "fake",
		HubURL:         "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node",
		TokenFile:      filepath.Join(t.TempDir(), "node-token"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := New(cfg, nil)
	n.readTimeout = 400 * time.Millisecond
	go func() { _ = n.Run(ctx) }()

	waitSignal(t, regs, 5*time.Second, "first registration")
	waitSignal(t, regs, 5*time.Second, "reconnect after silent link loss")
}

// TestNodeKeepsConnectionAliveAcrossHubPings is the other half of the same
// contract: the deadline must be refreshed by the hub's heartbeat, or a healthy
// idle node would tear down its connection every 90s. The scripted hub pings
// faster than the shortened deadline and the node must stay connected.
func TestNodeKeepsConnectionAliveAcrossHubPings(t *testing.T) {
	t.Setenv("FLEETDECK_EVENT_BUFFER_DIR", "")

	var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	hold := make(chan struct{})
	regs := make(chan struct{}, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var reg protocol.Envelope
		if err := c.ReadJSON(&reg); err != nil || reg.Type != protocol.MsgRegisterRequest {
			return
		}
		resp := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "pinging-hub"})
		if err := c.WriteJSON(resp); err != nil {
			return
		}
		select {
		case regs <- struct{}{}:
		default:
		}

		ping := time.NewTicker(100 * time.Millisecond)
		defer ping.Stop()
		for {
			select {
			case <-hold:
				return
			case <-ping.C:
				if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
					return
				}
			}
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer close(hold)

	cfg := Config{
		Name:           "pinged-node",
		Host:           "test",
		AgentTypes:     []string{"fake"},
		DefaultAdapter: "fake",
		HubURL:         "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node",
		TokenFile:      filepath.Join(t.TempDir(), "node-token"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := New(cfg, nil)
	n.readTimeout = 500 * time.Millisecond
	go func() { _ = n.Run(ctx) }()

	waitSignal(t, regs, 5*time.Second, "first registration")
	time.Sleep(3 * n.readTimeout)
	select {
	case <-regs:
		t.Fatal("node reconnected after pings refreshed its read deadline")
	default:
	}
}
