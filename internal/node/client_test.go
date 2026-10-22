package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/adapters"
	_ "fleetdeck/internal/adapters/fake" // registers agent_type "fake"
	"fleetdeck/internal/protocol"
)

// TestNodeReportsAdapterEventsToHub stands up a scripted hub, runs the real
// node daemon against it, and asserts the node registers, persists its issued
// token, runs the fake adapter on task.start, and reports both todo updates and
// the terminal session state back up the socket.
func TestNodeReportsAdapterEventsToHub(t *testing.T) {
	var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	todoCh := make(chan struct{}, 1)
	doneCh := make(chan struct{}, 1)
	startedCh := make(chan protocol.SessionStarted, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var reg protocol.Envelope
		if err := c.ReadJSON(&reg); err != nil {
			return
		}
		if reg.Type != protocol.MsgRegisterRequest {
			return
		}
		resp := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "test-node", Token: "test-token"})
		if err := c.WriteJSON(resp); err != nil {
			return
		}
		start := protocol.NewEnvelope(protocol.MsgTaskStart, protocol.TaskStartCommand{
			TaskID: "t1", SessionID: "s1", AgentType: "fake", Prompt: "hello", WorkspacePath: "/tmp/ws",
		})
		if err := c.WriteJSON(start); err != nil {
			return
		}

		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			switch env.Type {
			case protocol.MsgSessionStarted:
				var st protocol.SessionStarted
				if env.Decode(&st) == nil {
					select {
					case startedCh <- st:
					default:
					}
				}
			case protocol.MsgTodoUpdate:
				select {
				case todoCh <- struct{}{}:
				default:
				}
			case protocol.MsgEvent:
				var we struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if err := env.Decode(&we); err != nil {
					continue
				}
				if we.Type != protocol.EvSessionStateChanged {
					continue
				}
				var sc protocol.SessionStateChanged
				if json.Unmarshal(we.Data, &sc) != nil {
					continue
				}
				if sc.Status == protocol.SessionDone {
					select {
					case doneCh <- struct{}{}:
					default:
					}
				}
			}
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := Config{
		Name:           "test-node",
		Host:           "test",
		AgentTypes:     []string{"fake"},
		DefaultAdapter: "fake",
		HubURL:         "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node",
		TokenFile:      filepath.Join(t.TempDir(), "node-token"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = New(cfg, adapters.Default).Run(ctx) }()

	waitSignal(t, todoCh, 5*time.Second, "todo_update")
	waitSignal(t, doneCh, 5*time.Second, "session done event")

	// The hub persists this id to replay the conversation later; without it
	// GET /api/sessions/{id}/messages cannot reach the agent-side session.
	select {
	case st := <-startedCh:
		if st.SessionID != "s1" || st.ExternalSessionID != "fake-s1" {
			t.Fatalf("session.started = %+v, want s1 / fake-s1", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session.started")
	}

	if _, err := os.Stat(cfg.TokenFile); err != nil {
		t.Fatalf("expected issued token to be persisted at %s: %v", cfg.TokenFile, err)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for %s", d, what)
	}
}
