package node

import (
	"context"
	"fmt"
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

// TestNodeWritesHubURLBackAfterRegistration covers the one-time onboarding
// path: the operator passes --hub-url (SetHubURL) instead of writing it into
// the config, the node registers, and the endpoint is persisted so later runs
// need only --config. The join token must never be written to disk.
func TestNodeWritesHubURLBackAfterRegistration(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	registered := make(chan struct{}, 1)

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
		resp := protocol.NewEnvelope(protocol.MsgRegisterResponse, protocol.RegisterResponse{NodeID: "cfg-node", Token: "node-token"})
		if err := c.WriteJSON(resp); err != nil {
			return
		}
		select {
		case registered <- struct{}{}:
		default:
		}
		// Hold the connection open until the test cancels the context.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "node.config.yaml")
	tokenFile := filepath.Join(dir, "node-token")
	cfgYAML := fmt.Sprintf("name: cfg-node\n# keep me\ntoken_file: %s\n", tokenFile)
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hubURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/node"
	cfg.SetHubURL(hubURL)
	cfg.JoinToken = "join-secret-must-not-be-stored"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = New(cfg, adapters.Default).Run(ctx) }()

	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("node never registered")
	}

	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for {
		raw, err = os.ReadFile(configPath)
		if err == nil && strings.Contains(string(raw), "hub_url: "+hubURL) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub_url was not written back; file is %q", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}

	text := string(raw)
	if !strings.Contains(text, "name: cfg-node") || !strings.Contains(text, "# keep me") {
		t.Fatalf("write-back clobbered the rest of the config: %q", text)
	}
	if strings.Contains(text, cfg.JoinToken) {
		t.Fatalf("join token must never be persisted: %q", text)
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("issued node token not persisted: %v", err)
	}
}
