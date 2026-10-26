package node

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
)

// nodeReadTimeout bounds how long the node waits for any frame from the hub.
// The hub pings every 30s (hub's wsPingInterval), so a silently dropped link
// (no FIN/RST: NAT rebinding, one-way packet loss) would otherwise block
// ReadJSON forever and never reach Run's reconnect loop.
const nodeReadTimeout = 90 * time.Second

// nodeWriteTimeout bounds a control-frame write. A wedged write is itself a
// dead-link symptom, so it must surface as a connection error.
const nodeWriteTimeout = 10 * time.Second

// conn is a single established WebSocket. Writes are serialized so multiple
// goroutines may enqueue outbound messages safely.
type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// dial opens a WebSocket to url with a bounded handshake timeout. readTimeout
// is the liveness deadline refreshed by each hub ping.
func dial(ctx context.Context, url string, readTimeout time.Duration) (*conn, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	// The hub pings every 30s; each ping refreshes the deadline and is
	// answered with a pong (the default gorilla handler), so a link that stops
	// delivering frames times out instead of hanging the daemon silently.
	_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
	ws.SetPingHandler(func(data string) error {
		if err := ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}
		switch err := ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(nodeWriteTimeout)); {
		case err == nil, errors.Is(err, websocket.ErrCloseSent):
			return nil
		default:
			// A pong we cannot send means the link is gone: fail the read so
			// runOnce tears the connection down and Run reconnects.
			return err
		}
	})
	return &conn{ws: ws}, nil
}

func (c *conn) write(env protocol.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteJSON(env)
}

func (c *conn) read() (protocol.Envelope, error) {
	var env protocol.Envelope
	if err := c.ws.ReadJSON(&env); err != nil {
		return protocol.Envelope{}, err
	}
	return env, nil
}

func (c *conn) close() error { return c.ws.Close() }
