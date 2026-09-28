package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// Config is the node daemon's YAML configuration (examples/fake-node.config.yaml).
// Environment variables provide fallbacks where noted.
type Config struct {
	// Name is the stable display name the hub uses to identify this node
	// across reconnects. Required.
	Name string `yaml:"name"`
	// Host is a free-form host label reported to the hub. Defaults to the
	// machine hostname.
	Host string `yaml:"host"`
	// Capabilities are extra labels advertised to the hub.
	Capabilities []string `yaml:"capabilities"`
	// AgentTypes are the agent_type keys this node can run. They are also
	// advertised as capabilities.
	AgentTypes []string `yaml:"agent_types"`
	// HubURL is the hub's node WebSocket endpoint. When empty it is derived
	// from $FLEETDECK_HUB_ADDR as ws://ADDR/ws/node (LoadConfig errors when
	// neither is set).
	HubURL string `yaml:"hub_url"`
	// TokenFile is where the issued node token is persisted between runs.
	// Defaults to $FLEETDECK_NODE_TOKEN_FILE or .devenv/state/fleetdeck/node-token.
	TokenFile string `yaml:"token_file"`
	// DefaultAdapter is used when a task's agent_type is empty.
	DefaultAdapter string `yaml:"default_adapter"`
}

// LoadConfig reads and defaults a node configuration file. An empty path yields
// a fully-defaulted config.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("node: read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("node: parse config %s: %w", path, err)
		}
	}
	cfg.ApplyDefaults()
	if cfg.HubURL == "" {
		return Config{}, errors.New("node: hub_url is not set in the config and FLEETDECK_HUB_ADDR is unset")
	}
	return cfg, nil
}

// ApplyDefaults fills empty fields with their documented defaults.
func (c *Config) ApplyDefaults() {
	if c.HubURL == "" {
		if addr := os.Getenv("FLEETDECK_HUB_ADDR"); addr != "" {
			c.HubURL = "ws://" + addr + "/ws/node"
		}
	}
	if c.TokenFile == "" {
		c.TokenFile = os.Getenv("FLEETDECK_NODE_TOKEN_FILE")
	}
	if c.TokenFile == "" {
		c.TokenFile = filepath.Join(".devenv", "state", "fleetdeck", "node-token")
	}
	if c.Host == "" {
		if h, err := os.Hostname(); err == nil {
			c.Host = h
		}
	}
	if c.DefaultAdapter == "" && len(c.AgentTypes) > 0 {
		c.DefaultAdapter = c.AgentTypes[0]
	}
}

// advertisedCapabilities is the deduplicated union of Capabilities and
// AgentTypes, sent to the hub in RegisterRequest.
func (c *Config) advertisedCapabilities() []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(c.Capabilities)+len(c.AgentTypes))
	for _, group := range [][]string{c.Capabilities, c.AgentTypes} {
		for _, c := range group {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// Node is the daemon. Create one with New and run it with Run.
type Node struct {
	cfg      Config
	registry *adapters.Registry
	log      *slog.Logger

	// outbound writer plumbing; swapped on every reconnect. With buf != nil
	// (FLEETDECK_EVENT_BUFFER_DIR) envelopes are persisted first and the
	// writer replays the queue; otherwise the in-memory channel is used and
	// overflow is dropped.
	outMu   sync.Mutex
	out     chan protocol.Envelope
	outDone chan struct{}
	buf     *eventBuffer

	// running adapters, keyed by session id.
	sessMu   sync.Mutex
	sessions map[string]adapters.Adapter
}

// New builds a Node. A nil registry uses adapters.Default. When
// FLEETDECK_EVENT_BUFFER_DIR is set, outbound events are persisted there while
// the hub is unreachable and replayed on reconnect (see buffer.go).
func New(cfg Config, registry *adapters.Registry) *Node {
	if registry == nil {
		registry = adapters.Default
	}
	n := &Node{
		cfg:      cfg,
		registry: registry,
		log:      slog.Default().With("node", cfg.Name),
		sessions: make(map[string]adapters.Adapter),
	}
	if dir := os.Getenv("FLEETDECK_EVENT_BUFFER_DIR"); dir != "" {
		buf, err := openEventBuffer(dir)
		if err != nil {
			n.log.Warn("event buffer unavailable; events drop while the hub is unreachable",
				"dir", dir, "err", err)
		} else {
			n.buf = buf
			n.log.Info("event buffering enabled", "dir", dir)
		}
	}
	return n
}

// Run connects to the hub and serves commands until ctx is cancelled. It
// reconnects with exponential backoff (1s doubling to a 30s ceiling) on
// failure. Run returns when ctx is cancelled.
func (n *Node) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := n.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n.log.Warn("hub connection lost; reconnecting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce performs one connect/register/serve cycle. It returns when the
// connection drops or dies.
func (n *Node) runOnce(ctx context.Context) error {
	c, err := dial(ctx, n.cfg.HubURL)
	if err != nil {
		return fmt.Errorf("dial %s: %w", n.cfg.HubURL, err)
	}
	defer func() { _ = c.close() }()

	regEnv := protocol.NewEnvelope(protocol.MsgRegisterRequest, protocol.RegisterRequest{
		Name:         n.cfg.Name,
		Host:         n.cfg.Host,
		Capabilities: n.cfg.advertisedCapabilities(),
		Token:        n.readToken(),
	})
	if err := c.write(regEnv); err != nil {
		return fmt.Errorf("send register_request: %w", err)
	}

	respEnv, err := c.read()
	if err != nil {
		return fmt.Errorf("read register_response: %w", err)
	}
	if respEnv.Type != protocol.MsgRegisterResponse {
		return fmt.Errorf("unexpected first reply %q (want %s)", respEnv.Type, protocol.MsgRegisterResponse)
	}
	var resp protocol.RegisterResponse
	if err := respEnv.Decode(&resp); err != nil {
		return fmt.Errorf("decode register_response: %w", err)
	}
	if resp.Token != "" {
		if err := n.writeToken(resp.Token); err != nil {
			n.log.Warn("could not persist issued node token", "err", err, "path", n.cfg.TokenFile)
		}
	}
	n.log.Info("registered with hub", "node_id", resp.NodeID, "hub", n.cfg.HubURL)

	stop := n.startWriter(c)
	defer stop()

	for {
		env, err := c.read()
		if err != nil {
			return err
		}
		n.handle(ctx, env)
	}
}

// startWriter launches the single outbound writer goroutine and returns a
// stop function. Adapter sinks enqueue through enqueue; the writer serializes
// all writes onto the socket. With event buffering enabled the writer instead
// replays the on-disk queue (startBufferedWriter), which also covers envelopes
// persisted while the hub was unreachable.
func (n *Node) startWriter(c *conn) func() {
	if n.buf != nil {
		return n.startBufferedWriter(c)
	}
	out := make(chan protocol.Envelope, 128)
	done := make(chan struct{})

	n.outMu.Lock()
	n.out = out
	n.outDone = done
	n.outMu.Unlock()

	go func() {
		for {
			select {
			case <-done:
				return
			case env := <-out:
				if err := c.write(env); err != nil {
					return
				}
			}
		}
	}()

	return func() {
		n.outMu.Lock()
		if n.out == out {
			close(done)
			n.out = nil
			n.outDone = nil
		}
		n.outMu.Unlock()
	}
}

// enqueue queues an outbound message. Without event buffering it drops the
// message if the writer is gone or the buffer is full; with buffering it
// persists the message for replay either way. EventSink callbacks are expected
// to be non-blocking.
func (n *Node) enqueue(env protocol.Envelope) {
	if n.buf != nil {
		if err := n.buf.Push(env); err != nil {
			n.log.Warn("could not persist outbound message; dropping it",
				"type", env.Type, "err", err)
		}
		return
	}
	n.outMu.Lock()
	out, done := n.out, n.outDone
	n.outMu.Unlock()
	if out == nil {
		return
	}
	select {
	case <-done:
	case out <- env:
	default:
		n.log.Warn("dropping outbound message; writer buffer full", "type", env.Type)
	}
}

// startBufferedWriter runs the replay loop used when event buffering is
// enabled. Every envelope is on disk before it is attempted; each successful
// write removes its file, so a write failure leaves the envelope queued and
// the next connection resumes in sequence order. The loop also waits (bounded)
// when the queue is empty so new pushes wake it promptly.
func (n *Node) startBufferedWriter(c *conn) func() {
	done := make(chan struct{})
	go func() {
		for {
			env, path, ok := n.buf.Next()
			if !ok {
				select {
				case <-done:
					return
				case <-n.buf.wake:
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
			if err := c.write(env); err != nil {
				// Connection is dead; the envelope stays queued and the
				// reconnect's writer picks the queue back up.
				return
			}
			if err := n.buf.Remove(path); err != nil {
				n.log.Warn("could not remove delivered event file", "path", path, "err", err)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	return func() { close(done) }
}

func (n *Node) readToken() string {
	raw, err := os.ReadFile(n.cfg.TokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (n *Node) writeToken(token string) error {
	if dir := filepath.Dir(n.cfg.TokenFile); dir != "" {
		// MkdirAll is idempotent: an existing state dir is the happy path.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(n.cfg.TokenFile, []byte(token+"\n"), 0o600)
}
