package node

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
)

// conn is a single established WebSocket. Writes are serialized so multiple
// goroutines may enqueue outbound messages safely.
type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// dial opens a WebSocket to url with a bounded handshake timeout.
func dial(ctx context.Context, url string) (*conn, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
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
