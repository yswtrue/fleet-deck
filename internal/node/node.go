package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
	"fleetdeck/internal/version"
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
	// AgentTypes are the agent_type keys this node can run. They are
	// advertised to the hub so the task form offers exactly these.
	AgentTypes []string `yaml:"agent_types"`
	// HubURL is the hub's node WebSocket endpoint. When empty it is derived
	// from $FLEETDECK_HUB_ADDR as ws://ADDR/ws/node (LoadConfig errors when
	// neither is set).
	HubURL string `yaml:"hub_url"`
	// TokenFile is where the issued node token is persisted between runs.
	// Defaults to $FLEETDECK_NODE_TOKEN_FILE or .devenv/state/fleetdeck/node-token.
	TokenFile string `yaml:"token_file"`
	// JoinToken is the single-use pairing token a first registration must
	// present (minted in the UI under Nodes -> Add node). It is only needed
	// until token_file holds the node token; set it with --join-token (or the
	// join_token key). It is never written back to the file.
	JoinToken string `yaml:"join_token"`
	// MachineID overrides the detected machine fingerprint. The hub allows at
	// most one node per fingerprint; leave empty in production (it is derived
	// from the OS machine id and hashed). Defaults to
	// $FLEETDECK_NODE_MACHINE_ID.
	MachineID string `yaml:"machine_id"`
	// LockFile is the machine-wide singleton lock that prevents two node
	// daemons on one computer. Defaults to $FLEETDECK_NODE_LOCK_FILE or
	// $TMPDIR/fleetdeck-node.lock.
	LockFile string `yaml:"lock_file"`
	// DefaultAdapter is used when a task's agent_type is empty.
	DefaultAdapter string `yaml:"default_adapter"`

	// path is the config file LoadConfig read ("" when none). It is the target
	// of the hub_url write-back after a --hub-url registration.
	path string
	// persistHubURL records that HubURL came from --hub-url, so a successful
	// registration writes it into path.
	persistHubURL bool
}

// LoadConfig reads and defaults a node configuration file. An empty path yields
// a fully-defaulted config. Hub endpoint overrides (--hub-url) are applied by
// the caller, so LoadConfig does not reject a config without one — Validate
// does, after the overrides.
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
		cfg.path = path
	}
	cfg.ApplyDefaults()
	return cfg, nil
}

// Validate rejects a config that cannot connect to a hub. Call it after
// applying flag overrides.
func (c Config) Validate() error {
	if c.HubURL == "" {
		return errors.New("node: no hub endpoint: set hub_url in the config, pass --hub-url, or set FLEETDECK_HUB_ADDR")
	}
	return nil
}

// SetHubURL applies --hub-url and schedules a write-back to the config file
// once the hub accepts the registration, so later runs need only --config.
func (c *Config) SetHubURL(url string) {
	c.HubURL = url
	c.persistHubURL = true
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
	if c.MachineID == "" {
		c.MachineID = os.Getenv("FLEETDECK_NODE_MACHINE_ID")
	}
	if c.LockFile == "" {
		c.LockFile = os.Getenv("FLEETDECK_NODE_LOCK_FILE")
	}
	if c.LockFile == "" {
		c.LockFile = defaultLockFile()
	}
	if c.TokenFile == "" {
		c.TokenFile = DefaultTokenFile()
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

// advertisedAgentTypes is the configured adapter list sent to the hub. It is
// never nil: an explicit empty array tells the hub to clear a stale list,
// while an absent field (an older daemon) leaves the stored list untouched.
func (c *Config) advertisedAgentTypes() []string {
	seen := make(map[string]bool, len(c.AgentTypes))
	out := make([]string, 0, len(c.AgentTypes))
	for _, a := range c.AgentTypes {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
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

	// transcripts is the node-local live transcript cache (transcript.go). It
	// is the authoritative source for a running session's message order; the
	// hub proxies it for GET /api/sessions/{id}/messages.
	transcripts *transcriptStore

	// readTimeout bounds how long the node waits for a frame from the hub (see
	// dial). Production leaves it at nodeReadTimeout; tests shorten it.
	readTimeout time.Duration
}

// New builds a Node. A nil registry uses adapters.Default. When
// FLEETDECK_EVENT_BUFFER_DIR is set, outbound events are persisted there while
// the hub is unreachable and replayed on reconnect (see buffer.go).
func New(cfg Config, registry *adapters.Registry) *Node {
	if registry == nil {
		registry = adapters.Default
	}
	if cfg.MachineID == "" {
		cfg.MachineID = detectMachineID()
	}
	n := &Node{
		cfg:         cfg,
		registry:    registry,
		log:         slog.Default().With("node", cfg.Name),
		sessions:    make(map[string]adapters.Adapter),
		transcripts: newTranscriptStore(),
		readTimeout: nodeReadTimeout,
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

const (
	reconnectFloor = time.Second
	reconnectCeil  = 30 * time.Second
)

// Run connects to the hub and serves commands until ctx is cancelled. It
// reconnects with exponential backoff (1s doubling to a 30s ceiling, reset
// after every successful registration) on failure. Run returns when ctx is
// cancelled.
func (n *Node) Run(ctx context.Context) error {
	var delay reconnectDelay

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		registered, err := n.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if registered {
			// The hub accepted this process, so the link is healthy again:
			// the next outage must start from the floor instead of inheriting
			// the ceiling that an earlier, longer outage drove the backoff to.
			delay.reset()
		}
		wait := delay.next()
		n.log.Warn("hub connection lost; reconnecting", "err", err, "backoff", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// reconnectDelay is the node's reconnect backoff: start at 1s, double after
// each failed connection up to a 30s ceiling, and drop back to the floor once
// a registration succeeds.
type reconnectDelay struct{ d time.Duration }

// next returns the delay to wait before the next attempt and arms the one
// after it.
func (r *reconnectDelay) next() time.Duration {
	if r.d == 0 {
		r.d = reconnectFloor
	}
	wait := r.d
	if r.d < reconnectCeil {
		r.d *= 2
		if r.d > reconnectCeil {
			r.d = reconnectCeil
		}
	}
	return wait
}

// reset returns the backoff to the floor after a successful registration.
func (r *reconnectDelay) reset() { r.d = reconnectFloor }

// runOnce performs one connect/register/serve cycle. It returns when the
// connection drops or dies, and reports whether the hub accepted the
// registration so Run can reset its backoff after a healthy connection.
func (n *Node) runOnce(ctx context.Context) (registered bool, err error) {
	c, err := dial(ctx, n.cfg.HubURL, n.readTimeout)
	if err != nil {
		return false, fmt.Errorf("dial %s: %w", n.cfg.HubURL, err)
	}
	defer func() { _ = c.close() }()

	// Cancelling the context (SIGINT/SIGTERM) must unblock the read loop:
	// without this the daemon lingers on a dead connection and needs SIGKILL.
	connDone := make(chan struct{})
	defer close(connDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.ws.Close()
		case <-connDone:
		}
	}()

	regEnv := protocol.NewEnvelope(protocol.MsgRegisterRequest, protocol.RegisterRequest{
		Name:           n.cfg.Name,
		Host:           n.cfg.Host,
		AgentTypes:     n.cfg.advertisedAgentTypes(),
		DefaultAdapter: n.cfg.DefaultAdapter,
		Version:        version.Version,
		Token:          n.readToken(),
		JoinToken:      n.cfg.JoinToken,
		MachineID:      n.cfg.MachineID,
		Sessions:       n.liveSessionIDs(),
	})
	if err := c.write(regEnv); err != nil {
		return false, fmt.Errorf("send register_request: %w", err)
	}

	respEnv, err := c.read()
	if err != nil {
		return false, fmt.Errorf("read register_response: %w", err)
	}
	if respEnv.Type != protocol.MsgRegisterResponse {
		return false, fmt.Errorf("unexpected first reply %q (want %s)", respEnv.Type, protocol.MsgRegisterResponse)
	}
	var resp protocol.RegisterResponse
	if err := respEnv.Decode(&resp); err != nil {
		return false, fmt.Errorf("decode register_response: %w", err)
	}
	if resp.Error != "" {
		return false, fmt.Errorf("hub rejected registration: %s", resp.Error)
	}
	if resp.Token != "" {
		if err := n.writeToken(resp.Token); err != nil {
			n.log.Warn("could not persist issued node token", "err", err, "path", n.cfg.TokenFile)
		}
	}
	n.log.Info("registered with hub", "node_id", resp.NodeID, "hub", n.cfg.HubURL)
	registered = true

	// --hub-url is a one-time bootstrap: once the hub accepted us, remember the
	// endpoint in the config file so the next start needs only --config.
	if n.cfg.persistHubURL {
		if err := persistHubURL(n.cfg.path, n.cfg.HubURL); err != nil {
			n.log.Warn("could not write hub_url back to the config file", "err", err, "path", n.cfg.path)
		} else {
			n.log.Info("hub_url written to the config file", "path", n.cfg.path)
			n.cfg.persistHubURL = false
		}
	}

	stop := n.startWriter(c)
	defer stop()
	// A reconnect is the moment a lost ask becomes recoverable: the hub has
	// forgotten the prompt, so re-emit whatever the live adapters are paused on.
	n.reaskPending(ctx)

	for {
		env, err := c.read()
		if err != nil {
			return registered, err
		}
		n.handle(ctx, env)
	}
}

// liveSessionIDs snapshots the sessions whose adapters are still running in
// this process. The register handshake carries the list so the hub can tell a
// reconnect (adapters kept) from a restart or upgrade (adapters gone): without
// it, the reconnect cancels the offline cascade and sessions the dead process
// left behind stay "running" forever.
func (n *Node) liveSessionIDs() []string {
	n.sessMu.Lock()
	defer n.sessMu.Unlock()
	ids := make([]string, 0, len(n.sessions))
	for id := range n.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// reaskPending re-emits each live session's unanswered user-input request.
// The node calls it right after (re)registering: the hub only knows about asks
// that arrived over the wire, and a frame lost while the link was down would
// leave the operator without a picker and the agent waiting forever.
func (n *Node) reaskPending(ctx context.Context) {
	n.sessMu.Lock()
	live := make(map[string]adapters.Adapter, len(n.sessions))
	for id, a := range n.sessions {
		live[id] = a
	}
	n.sessMu.Unlock()
	if len(live) == 0 {
		return
	}
	sink := &connSink{n: n}
	for id, a := range live {
		asker, ok := a.(adapters.PendingAsker)
		if !ok {
			continue
		}
		prompt, pending := asker.PendingAsk()
		if !pending {
			continue
		}
		n.log.Info("re-asking user input after reconnect", "session_id", id)
		sink.StateChange(ctx, id, protocol.SessionAwaitingUser)
		sink.UserInputRequested(ctx, id, prompt)
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
					// A dead socket must end the connection, not just this
					// writer: otherwise the read loop keeps blocking and every
					// later event is silently dropped.
					_ = c.ws.Close()
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
				// reconnect's writer picks the queue back up. Close the socket
				// so the read loop unblocks into a reconnect instead of
				// lingering with every later event dropped.
				_ = c.ws.Close()
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
