package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
	"fleetdeck/internal/protocol"
	"fleetdeck/internal/push"
	"fleetdeck/internal/store"
)

// Cache entry TTLs. Both are safety nets behind explicit invalidation: a
// missed delete serves stale data for at most this long. authCacheTTL also
// bounds how long a token revoked in bulk (password reset) keeps working.
const (
	authCacheTTL = 60 * time.Second
	taskListTTL  = 30 * time.Second
)

// authCacheEntry is the cached result of resolving a bearer token: its owner
// plus the token's own expiry, re-checked on every cache hit.
type authCacheEntry struct {
	User      store.User `json:"user"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// Hub is the control plane. It owns the database, the adapter registry, the
// web broadcaster, and the set of currently connected nodes.
type Hub struct {
	store    *store.Store
	registry *adapters.Registry
	bus      *Broadcaster
	log      *slog.Logger
	cache    cache.Cache
	bindings *bindingCache

	nodesMu sync.Mutex
	nodes   map[string]*nodeConn

	// textMu guards the in-memory session text buffers (see textbuffer.go).
	textMu sync.Mutex
	text   map[string]textBufferEntry

	// histWait correlates proxied session-history requests with the node's
	// history responses (see history.go).
	histMu   sync.Mutex
	histWait map[string]chan protocol.SessionHistoryResponse

	// transcriptWait correlates live-transcript requests with the owning
	// node's responses. The node is the authoritative buffer while a session
	// runs (see history.go).
	transcriptMu   sync.Mutex
	transcriptWait map[string]chan protocol.SessionTranscriptResponse

	// fsWait correlates proxied directory-listing requests with the node's
	// fs.list_resp payloads (see fs.go).
	fsMu   sync.Mutex
	fsWait map[string]chan protocol.FSListResponse

	// joinToken is the optional deployment-level pairing secret
	// (FLEETDECK_NODE_JOIN_TOKEN). It lets automated deployments register
	// nodes without a database-backed token; UI-minted tokens remain the
	// normal path.
	joinToken string

	// offlineMu/offlineTimers debounce the offline cascade: a node that
	// reconnects within offlineGrace keeps its running sessions instead of
	// having them failed by a one-second network blip.
	offlineMu     sync.Mutex
	offlineTimers map[string]*time.Timer
	offlineGrace  time.Duration

	// Web Push: the VAPID sender is nil when the key could not be loaded
	// (push is then disabled, never a startup failure), the queue buffers
	// task-status events for the delivery worker, and pushSem bounds
	// concurrent outbound HTTP requests.
	pushSender *push.Sender
	pushQueue  chan protocol.Event
	pushStop   chan struct{}
	pushOnce   sync.Once
	pushSem    chan struct{}
}

// nodeOfflineGrace is how long a node may stay disconnected before its running
// sessions are failed. The node keeps its adapter processes alive across a
// reconnect, so a short outage must not destroy a task.
const nodeOfflineGrace = 30 * time.Second

// nodeConn is the hub's side of a registered node connection: a buffered
// outbound queue drained by a single writer goroutine.
type nodeConn struct {
	id   string
	ws   *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (nc *nodeConn) close() { nc.once.Do(func() { close(nc.done) }) }

// New builds a Hub. A nil registry falls back to an empty one; a nil cache
// falls back to a no-op cache (every read goes to Postgres).
func New(ctx context.Context, st *store.Store, registry *adapters.Registry, c cache.Cache) *Hub {
	if registry == nil {
		registry = adapters.NewRegistry()
	}
	if c == nil {
		c = cache.Noop{}
	}
	h := &Hub{
		store:          st,
		registry:       registry,
		log:            slog.Default().With("component", "hub"),
		cache:          c,
		bindings:       newBindingCache(),
		nodes:          make(map[string]*nodeConn),
		offlineTimers:  make(map[string]*time.Timer),
		offlineGrace:   nodeOfflineGrace,
		text:           make(map[string]textBufferEntry),
		histWait:       make(map[string]chan protocol.SessionHistoryResponse),
		transcriptWait: make(map[string]chan protocol.SessionTranscriptResponse),
		fsWait:         make(map[string]chan protocol.FSListResponse),
		// Deployment-level pairing secret for automated nodes; unset means only
		// UI-minted single-use tokens can register a new node.
		joinToken: os.Getenv("FLEETDECK_NODE_JOIN_TOKEN"),
	}
	// Broadcast fan-out is scoped by the node that owns each event; the
	// resolver runs once per event (see eventNodeID).
	h.bus = NewBroadcaster(h.eventNodeID)
	// A fresh hub has no connections: any row still marked online belongs to
	// a previous process that died or restarted without disconnect frames.
	// Reset the persisted status (sessions are untouched so a node that
	// reconnects keeps its live adapters).
	if err := st.Queries().MarkNodesOffline(ctx); err != nil {
		h.log.Warn("reset node statuses", "err", err)
	}
	h.initPush(ctx)
	return h
}

// Store exposes the underlying store.
func (h *Hub) Store() *store.Store { return h.store }

// Registry exposes the adapter registry the hub advertises task types from.
func (h *Hub) Registry() *adapters.Registry { return h.registry }

// Bus exposes the web broadcaster.
func (h *Hub) Bus() *Broadcaster { return h.bus }

// Close releases broadcaster subscribers. It does not close the store.
func (h *Hub) Close() error {
	h.pushOnce.Do(func() {
		if h.pushStop != nil {
			close(h.pushStop)
		}
	})
	h.bus.Close()
	return nil
}

// ---- read-through cache ----------------------------------------------------

// cachedJSON reports whether key holds a cached value, decoding it into dest.
// Cache errors are treated as misses: the cache is an accelerator only.
func (h *Hub) cachedJSON(ctx context.Context, key string, dest any) bool {
	ok, err := h.cache.Get(ctx, key, dest)
	if err != nil {
		h.log.Debug("cache get failed; reading through", "key", key, "err", err)
		return false
	}
	return ok
}

// cacheSet stores val under key; failures are logged and ignored.
func (h *Hub) cacheSet(ctx context.Context, key string, val any, ttl time.Duration) {
	if err := h.cache.Set(ctx, key, val, ttl); err != nil {
		h.log.Debug("cache set failed", "key", key, "err", err)
	}
}

// cacheDelete drops keys; failures are logged and ignored.
func (h *Hub) cacheDelete(ctx context.Context, keys ...string) {
	if err := h.cache.Delete(ctx, keys...); err != nil {
		h.log.Debug("cache delete failed", "keys", keys, "err", err)
	}
}

// invalidateTasks drops the cached task list for a workspace. Every task
// mutation (create, status change, sort, dispatch, session-derived moves,
// node-offline cascade) must call it; taskListTTL covers anything missed.
func (h *Hub) invalidateTasks(ctx context.Context, workspaceID string) {
	if workspaceID == "" {
		return
	}
	h.cacheDelete(ctx, "tasks:ws:"+workspaceID)
}

// ---- node connection registry ---------------------------------------------

func (h *Hub) addNode(id string, ws *websocket.Conn) *nodeConn {
	nc := &nodeConn{id: id, ws: ws, send: make(chan []byte, 64), done: make(chan struct{})}
	h.nodesMu.Lock()
	if old, ok := h.nodes[id]; ok {
		old.close()
		// Stop the superseded reader now: it must not linger until its read
		// deadline and then tear down this connection during cleanup.
		_ = old.ws.Close()
	}
	h.nodes[id] = nc
	h.nodesMu.Unlock()
	// The node is back: a pending offline cascade must not fire.
	h.cancelOffline(id)
	return nc
}

// scheduleOffline runs the offline cascade (fail running sessions, flip their
// tasks) once the node has been gone for offlineGrace. A reconnect cancels it.
func (h *Hub) scheduleOffline(nodeID string) {
	h.offlineMu.Lock()
	defer h.offlineMu.Unlock()
	if t, ok := h.offlineTimers[nodeID]; ok {
		t.Stop()
	}
	h.offlineTimers[nodeID] = time.AfterFunc(h.offlineGrace, func() {
		h.offlineMu.Lock()
		delete(h.offlineTimers, nodeID)
		h.offlineMu.Unlock()
		if h.nodeOnline(nodeID) {
			h.log.Debug("offline cascade cancelled: node reconnected", "node_id", nodeID)
			return
		}
		h.log.Info("offline cascade: node stayed offline", "node_id", nodeID)
		h.markNodeOffline(context.Background(), nodeID)
	})
}

// cancelOffline drops a pending offline cascade for a node that reconnected.
func (h *Hub) cancelOffline(nodeID string) {
	h.offlineMu.Lock()
	defer h.offlineMu.Unlock()
	if t, ok := h.offlineTimers[nodeID]; ok {
		t.Stop()
		delete(h.offlineTimers, nodeID)
	}
}

// removeNode drops nc and stops its writer. It reports false when a newer
// connection has already taken over the node id: the stale handler must not
// evict the live connection (that would stop its pings and kill the node's
// reconnect a read-timeout later).
func (h *Hub) removeNode(nc *nodeConn) bool {
	h.nodesMu.Lock()
	defer h.nodesMu.Unlock()
	if cur, ok := h.nodes[nc.id]; !ok || cur != nc {
		return false
	}
	delete(h.nodes, nc.id)
	nc.close()
	return true
}

// liveNodeStatus reports a node's actual connectivity. The in-memory
// connection registry is authoritative; the database column is only the last
// written state, so a hub restart (or a missed disconnect) must not make a
// node look online. Callers that render node rows overlay this on the row.
func (h *Hub) liveNodeStatus(id string) protocol.NodeStatus {
	if h.nodeOnline(id) {
		return protocol.NodeOnline
	}
	return protocol.NodeOffline
}

// nodeOnline reports whether a node currently has a live WebSocket.
func (h *Hub) nodeOnline(id string) bool {
	h.nodesMu.Lock()
	nc, ok := h.nodes[id]
	h.nodesMu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-nc.done:
		return false
	default:
		return true
	}
}

// sendToNode enqueues an outbound message for node id. It reports false when the
// node is not connected or its queue is full.
func (h *Hub) sendToNode(id string, env protocol.Envelope) bool {
	raw, err := json.Marshal(env)
	if err != nil {
		h.log.Warn("encode node message", "err", err, "type", env.Type)
		return false
	}
	h.nodesMu.Lock()
	nc, ok := h.nodes[id]
	h.nodesMu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-nc.done:
		return false
	default:
	}
	select {
	case <-nc.done:
		return false
	case nc.send <- raw:
		return true
	default:
		return false
	}
}

// storedTokenHash reads node.token_hash directly: the public store API does not
// surface the secret (protocol.Node deliberately omits it), and bcrypt hashes
// are not queryable by value.
func (h *Hub) storedTokenHash(ctx context.Context, nodeID string) (string, error) {
	var hash string
	err := h.store.Pool().QueryRow(ctx, `SELECT token_hash FROM node WHERE id = $1`, nodeID).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("hub: read node token hash: %w", err)
	}
	return hash, nil
}

// registerNode authenticates an existing node or claims a new one. A name that
// has never registered requires a valid join token; an existing node must
// present the node token it was issued. On success the node is marked online
// and a freshly issued token is returned exactly once.
func (h *Hub) registerNode(ctx context.Context, req protocol.RegisterRequest) (protocol.Node, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return protocol.Node{}, "", errors.New("hub: register: name is required")
	}
	req.MachineID = strings.TrimSpace(req.MachineID)
	if len(req.MachineID) > 200 {
		return protocol.Node{}, "", errors.New("hub: register: machine_id is too long")
	}
	q := h.store.Queries()

	existing, err := q.GetNodeByName(ctx, name)
	if err == nil {
		return h.acceptExistingNode(ctx, existing, req)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return protocol.Node{}, "", err
	}
	return h.registerNewNode(ctx, name, req)
}

// Machine-identity errors. A machine fingerprint is claimed by exactly one
// node row; these make the rejection understandable in the node's log.
var (
	errMachineIDRequired = errors.New("hub: register: machine_id is required for a new node")
	errMachineTaken      = errors.New("hub: register: this machine is already registered with another node")
	errMachineMismatch   = errors.New("hub: register: this node is registered from a different machine")
)

// machineTakenError names the existing node so the operator can find it.
func machineTakenError(name string) error {
	return fmt.Errorf("hub: register: this machine is already registered as %q", name)
}

// claimMachineID attaches the requesting machine fingerprint to a node. A node
// that already claims the same fingerprint is idempotent; a node claiming a
// different one, or a fingerprint already owned by another node, is rejected.
// Callers wrap it in the same transaction as node creation.
func (h *Hub) claimMachineID(ctx context.Context, q *store.Queries, nodeID, machineID string) error {
	if machineID == "" {
		// Legacy daemons that predate the fingerprint still reconnect; they
		// adopt one on their first upgraded handshake.
		return nil
	}
	current, err := q.GetNodeMachineID(ctx, nodeID)
	if err != nil {
		return err
	}
	if current == machineID {
		return nil
	}
	if current != "" {
		return errMachineMismatch
	}
	other, err := q.GetNodeByMachineID(ctx, machineID)
	if err == nil && other.ID != nodeID {
		return machineTakenError(other.Name)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := q.UpdateNodeMachineID(ctx, nodeID, machineID); err != nil {
		if isConstraintViolation(err, "node_machine_id_key") {
			return errMachineTaken
		}
		return err
	}
	return nil
}

// isConstraintViolation reports whether err is a Postgres unique-constraint
// violation for the given constraint/index name.
func isConstraintViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// registerNewNode claims an unused join token and creates the node row in one
// transaction: the claim is burned only if the node insert succeeds, and the
// token's creator is bound to the new node (the static deployment token has no
// owner). The node receives a fresh node token for later reconnects.
func (h *Hub) registerNewNode(ctx context.Context, name string, req protocol.RegisterRequest) (protocol.Node, string, error) {
	if req.MachineID == "" {
		return protocol.Node{}, "", errMachineIDRequired
	}
	// The column is NOT NULL; a first handshake that omits labels means none.
	if req.Capabilities == nil {
		req.Capabilities = []string{}
	}
	token, hash, err := newToken()
	if err != nil {
		return protocol.Node{}, "", err
	}
	var created protocol.Node
	err = h.store.WithTx(ctx, func(q *store.Queries) error {
		jt, static, err := h.authorizeJoin(ctx, q, req.JoinToken)
		if err != nil {
			return err
		}
		// Reject a second identity for the same computer before consuming the
		// join token; the unique index still guards the concurrent race.
		if other, err := q.GetNodeByMachineID(ctx, req.MachineID); err == nil {
			return machineTakenError(other.Name)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		node, err := q.CreateNode(ctx, store.CreateNodeParams{
			Name:           name,
			Host:           req.Host,
			Status:         protocol.NodeOnline,
			AgentTypes:     req.AgentTypes,
			DefaultAdapter: req.DefaultAdapter,
			Capabilities:   req.Capabilities,
			TokenHash:      hash,
			MachineID:      req.MachineID,
		})
		if err != nil {
			return err
		}
		created = node
		return linkJoinToken(ctx, q, jt, static, node.ID)
	})
	if err != nil {
		// Includes losing the unique-name or unique-machine race to a
		// concurrent registration: the loser is rejected rather than silently
		// taking over that node.
		if isConstraintViolation(err, "node_machine_id_key") {
			return protocol.Node{}, "", errMachineTaken
		}
		return protocol.Node{}, "", err
	}
	return created, token, nil
}

// acceptExistingNode authenticates a reconnecting node against its stored token
// hash. A row without a hash (legacy pre-provisioning) is still unclaimed and
// needs a join token, exactly like a brand-new name.
func (h *Hub) acceptExistingNode(ctx context.Context, existing protocol.Node, req protocol.RegisterRequest) (protocol.Node, string, error) {
	hash, err := h.storedTokenHash(ctx, existing.ID)
	if err != nil {
		return protocol.Node{}, "", err
	}
	if hash == "" {
		return h.claimExistingNode(ctx, existing, req)
	}
	if !CheckToken(req.Token, hash) {
		return protocol.Node{}, "", errors.New("hub: register: invalid token")
	}
	// The fingerprint travels with every handshake so legacy rows can adopt
	// one, and a copied token file cannot move a node to another machine.
	if err := h.claimMachineID(ctx, h.store.Queries(), existing.ID, req.MachineID); err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.syncNodeAgents(ctx, &existing, req); err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.syncNodeCapabilities(ctx, &existing, req); err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.store.Queries().UpdateNodeStatus(ctx, existing.ID, protocol.NodeOnline, time.Now().UTC()); err != nil {
		return protocol.Node{}, "", err
	}
	existing.Status = protocol.NodeOnline
	return existing, "", nil
}

// syncNodeAgents refreshes a reconnecting node's runnable adapter list and
// default. A nil AgentTypes means the daemon predates the field; keep the
// stored binding rather than wiping it (such a daemon omits default_adapter
// too). Current daemons always send an array (possibly empty).
func (h *Hub) syncNodeAgents(ctx context.Context, node *protocol.Node, req protocol.RegisterRequest) error {
	if req.AgentTypes == nil {
		return nil
	}
	if err := h.store.Queries().UpdateNodeAgents(ctx, node.ID, req.AgentTypes, req.DefaultAdapter); err != nil {
		return err
	}
	node.AgentTypes = req.AgentTypes
	node.DefaultAdapter = req.DefaultAdapter
	return nil
}

// syncNodeCapabilities refreshes a reconnecting node's free-form labels. A nil
// slice means the daemon predates the field; keep the stored labels.
func (h *Hub) syncNodeCapabilities(ctx context.Context, node *protocol.Node, req protocol.RegisterRequest) error {
	if req.Capabilities == nil {
		return nil
	}
	if err := h.store.Queries().UpdateNodeCapabilities(ctx, node.ID, req.Capabilities); err != nil {
		return err
	}
	node.Capabilities = req.Capabilities
	return nil
}

// claimExistingNode issues the first node token for a legacy hash-less row and
// burns the presented join token in the same transaction.
func (h *Hub) claimExistingNode(ctx context.Context, existing protocol.Node, req protocol.RegisterRequest) (protocol.Node, string, error) {
	token, hash, err := newToken()
	if err != nil {
		return protocol.Node{}, "", err
	}
	err = h.store.WithTx(ctx, func(q *store.Queries) error {
		jt, static, err := h.authorizeJoin(ctx, q, req.JoinToken)
		if err != nil {
			return err
		}
		if err := h.claimMachineID(ctx, q, existing.ID, req.MachineID); err != nil {
			return err
		}
		if err := q.UpdateNodeTokenHash(ctx, existing.ID, hash); err != nil {
			return err
		}
		return linkJoinToken(ctx, q, jt, static, existing.ID)
	})
	if err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.syncNodeAgents(ctx, &existing, req); err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.syncNodeCapabilities(ctx, &existing, req); err != nil {
		return protocol.Node{}, "", err
	}
	if err := h.store.Queries().UpdateNodeStatus(ctx, existing.ID, protocol.NodeOnline, time.Now().UTC()); err != nil {
		return protocol.Node{}, "", err
	}
	existing.Status = protocol.NodeOnline
	return existing, token, nil
}

func newToken() (token, hash string, err error) {
	token, err = GenerateToken()
	if err != nil {
		return "", "", err
	}
	hash, err = HashToken(token)
	if err != nil {
		return "", "", err
	}
	return token, hash, nil
}

// markNodeOffline flips a node's status, broadcasts the change, and cascades
// the node's non-terminal sessions to failed (which completes their tasks).
func (h *Hub) markNodeOffline(ctx context.Context, nodeID string) {
	// The caller's request context may already be cancelled once the node's
	// socket is gone; use a detached, bounded context so the cascade still runs.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	q := h.store.Queries()
	if err := q.UpdateNodeStatus(ctx, nodeID, protocol.NodeOffline, time.Now().UTC()); err != nil {
		h.log.Warn("mark node offline", "node_id", nodeID, "err", err)
	}
	h.bus.Publish(protocol.Event{
		Type: protocol.EvNodeStatusChanged,
		Data: protocol.NodeStatusChanged{NodeID: nodeID, Status: protocol.NodeOffline},
	})

	sessions, err := q.ListSessionsByNode(ctx, nodeID)
	if err != nil {
		h.log.Warn("list sessions for offline node", "node_id", nodeID, "err", err)
		return
	}
	for _, s := range sessions {
		h.failSession(ctx, s, "node offline")
	}
}

// reconcileNodeSessions fails the non-terminal sessions the hub still believes
// are running on a (re)connecting node but that the daemon no longer holds.
// Adapters live in the node process, so a reconnect keeps them and the
// handshake lists them; an upgraded or crashed daemon comes back with none.
// Without this reconcile, that reconnect cancels the offline cascade while the
// new process never reports the loss, and those tasks sit in running forever.
//
// live is nil for a daemon that predates the field: it cannot tell us which
// adapters survived, so nothing is failed on guesswork. The caller runs this
// before the connection joins the registry, so a task dispatched to the
// returning node cannot be caught by a stale list.
func (h *Hub) reconcileNodeSessions(ctx context.Context, nodeID string, live []string) {
	if live == nil {
		return
	}
	keep := make(map[string]struct{}, len(live))
	for _, id := range live {
		if id != "" {
			keep[id] = struct{}{}
		}
	}
	sessions, err := h.store.Queries().ListSessionsByNode(ctx, nodeID)
	if err != nil {
		h.log.Warn("list sessions for session reconcile", "node_id", nodeID, "err", err)
		return
	}
	for _, s := range sessions {
		if _, ok := keep[s.ID]; ok {
			continue
		}
		h.failSession(ctx, s, "node restarted without the adapter")
	}
}

// failSession marks one non-terminal session failed, broadcasts the state
// change, and moves its task to the derived status. It is the shared write
// path of the offline cascade and the registration reconcile; terminal
// sessions and tasks already in the derived status are left alone.
func (h *Hub) failSession(ctx context.Context, s protocol.Session, reason string) {
	if s.Status == protocol.SessionDone || s.Status == protocol.SessionFailed {
		return
	}
	q := h.store.Queries()
	ended := time.Now().UTC()
	if err := q.UpdateSessionStatus(ctx, s.ID, protocol.SessionFailed, &ended); err != nil {
		h.log.Warn("fail session", "session_id", s.ID, "reason", reason, "err", err)
		return
	}
	h.log.Info("session failed", "session_id", s.ID, "reason", reason)
	h.bus.Publish(protocol.Event{
		Type: protocol.EvSessionStateChanged,
		Data: protocol.SessionStateChanged{SessionID: s.ID, Status: protocol.SessionFailed},
	})

	task, err := q.GetTaskBySessionID(ctx, s.ID)
	if err != nil {
		return
	}
	newStatus := taskStatusFor(protocol.SessionFailed)
	if task.Status == newStatus {
		return
	}
	updated, err := q.UpdateTaskStatus(ctx, task.ID, newStatus)
	if err != nil {
		h.log.Warn("fail task after session failure", "task_id", task.ID, "err", err)
		return
	}
	h.invalidateTasks(ctx, task.WorkspaceID)
	h.bus.Publish(protocol.Event{
		Type: protocol.EvTaskStatusChanged,
		Data: protocol.TaskStatusChanged{TaskID: updated.ID, From: task.Status, To: updated.Status},
	})
}

// dispatchTask opens a session for a freshly created task and tells the owning
// node to start it. The hub owns the session row; the node only runs the
// adapter. If the node is offline the task is left in waiting.
func (h *Hub) dispatchTask(ctx context.Context, task protocol.Task) (protocol.Task, error) {
	q := h.store.Queries()

	workspace, err := q.GetWorkspace(ctx, task.WorkspaceID)
	if err != nil {
		return task, err
	}
	if !h.nodeOnline(workspace.NodeID) {
		h.log.Warn("task queued but node offline; task stays waiting",
			"task_id", task.ID, "node_id", workspace.NodeID)
		return task, nil
	}

	// Session creation, the task's forward pointer, and the running flip are
	// one atomic unit: a crash in between must not leave a task pointing at a
	// session that half-exists.
	var session protocol.Session
	var updated protocol.Task
	err = h.store.WithTx(ctx, func(q *store.Queries) error {
		var err error
		session, err = q.CreateSession(ctx, store.CreateSessionParams{
			TaskID:    task.ID,
			NodeID:    workspace.NodeID,
			AgentType: task.AgentType,
			Status:    protocol.SessionRunning,
		})
		if err != nil {
			return err
		}
		if err := q.UpdateTaskSessionID(ctx, task.ID, session.ID); err != nil {
			return err
		}
		updated, err = q.UpdateTaskStatus(ctx, task.ID, protocol.TaskRunning)
		return err
	})
	if err != nil {
		return task, err
	}
	h.invalidateTasks(ctx, task.WorkspaceID)

	h.bus.Publish(protocol.Event{
		Type: protocol.EvTaskStatusChanged,
		Data: protocol.TaskStatusChanged{TaskID: task.ID, From: task.Status, To: protocol.TaskRunning},
	})
	h.bus.Publish(protocol.Event{
		Type: protocol.EvSessionStateChanged,
		Data: protocol.SessionStateChanged{SessionID: session.ID, Status: protocol.SessionRunning},
	})
	// The instruction is the first transcript line. Without it a live session
	// opens empty and the operator cannot see what the agent was asked to do.
	// The same message object travels to the node so its local transcript
	// carries the hub's id.
	var promptMessage *protocol.Message
	if prompt := strings.TrimSpace(task.Prompt); prompt != "" {
		inserted := h.recordUserMessage(ctx, session.ID, prompt)
		promptMessage = &inserted
		h.bus.Publish(protocol.Event{
			Type: protocol.EvSessionMessage,
			Data: protocol.SessionMessage{SessionID: session.ID, Message: inserted},
		})
	}

	env := protocol.NewEnvelope(protocol.MsgTaskStart, protocol.TaskStartCommand{
		TaskID:        task.ID,
		SessionID:     session.ID,
		AgentType:     task.AgentType,
		Prompt:        task.Prompt,
		WorkspacePath: workspace.Path,
		UserMessage:   promptMessage,
	})
	if !h.sendToNode(workspace.NodeID, env) {
		h.log.Warn("could not deliver task.start to node", "task_id", task.ID, "node_id", workspace.NodeID)
	}
	return updated, nil
}
