package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/cache"
	"fleetdeck/internal/protocol"
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
}

// nodeConn is the hub's side of a registered node connection: a buffered
// outbound queue drained by a single writer goroutine.
type nodeConn struct {
	id   string
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (nc *nodeConn) close() { nc.once.Do(func() { close(nc.done) }) }

// New builds a Hub. A nil registry falls back to an empty one; a nil cache
// falls back to a no-op cache (every read goes to Postgres).
func New(ctx context.Context, st *store.Store, registry *adapters.Registry, c cache.Cache) *Hub {
	_ = ctx
	if registry == nil {
		registry = adapters.NewRegistry()
	}
	if c == nil {
		c = cache.Noop{}
	}
	h := &Hub{
		store:    st,
		registry: registry,
		log:      slog.Default().With("component", "hub"),
		cache:    c,
		bindings: newBindingCache(),
		nodes:    make(map[string]*nodeConn),
		text:     make(map[string]textBufferEntry),
		histWait: make(map[string]chan protocol.SessionHistoryResponse),
	}
	// Broadcast fan-out is scoped by the node that owns each event; the
	// resolver runs once per event (see eventNodeID).
	h.bus = NewBroadcaster(h.eventNodeID)
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

func (h *Hub) addNode(id string) *nodeConn {
	nc := &nodeConn{id: id, send: make(chan []byte, 64), done: make(chan struct{})}
	h.nodesMu.Lock()
	if old, ok := h.nodes[id]; ok {
		old.close()
	}
	h.nodes[id] = nc
	h.nodesMu.Unlock()
	return nc
}

func (h *Hub) removeNode(id string) {
	h.nodesMu.Lock()
	if nc, ok := h.nodes[id]; ok {
		delete(h.nodes, id)
		nc.close()
	}
	h.nodesMu.Unlock()
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

func (h *Hub) nodeOnline(id string) bool {
	h.nodesMu.Lock()
	_, ok := h.nodes[id]
	h.nodesMu.Unlock()
	return ok
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

// registerNode authenticates or creates a node from a RegisterRequest. When a
// node is new (or had no token yet) it issues a fresh token; the caller must
// return it to the node exactly once. On success the node is marked online.
func (h *Hub) registerNode(ctx context.Context, req protocol.RegisterRequest) (protocol.Node, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return protocol.Node{}, "", errors.New("hub: register: name is required")
	}
	q := h.store.Queries()

	existing, err := q.GetNodeByName(ctx, name)
	if err == nil {
		return h.acceptExistingNode(ctx, existing, req.Token)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return protocol.Node{}, "", err
	}

	// New node: issue a token and create the row.
	token, hash, err := newToken()
	if err != nil {
		return protocol.Node{}, "", err
	}
	created, cerr := q.CreateNode(ctx, store.CreateNodeParams{
		Name:         name,
		Host:         req.Host,
		Status:       protocol.NodeOnline,
		Capabilities: req.Capabilities,
		TokenHash:    hash,
	})
	if cerr == nil {
		return created, token, nil
	}

	// Lost the create race: a concurrent registration inserted the same name.
	// The node_name_key unique violation means the row now exists; fall back to
	// it so both registrations converge on one identity.
	var pgErr *pgconn.PgError
	if errors.As(cerr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "node_name_key" {
		existing, lerr := q.GetNodeByName(ctx, name)
		if lerr != nil {
			return protocol.Node{}, "", cerr
		}
		return h.acceptExistingNode(ctx, existing, req.Token)
	}
	return protocol.Node{}, "", cerr
}

// acceptExistingNode authenticates a reconnecting node against its stored token
// hash, or issues the node's first token, then marks it online. It returns the
// plaintext token only when a new one was issued.
func (h *Hub) acceptExistingNode(ctx context.Context, existing protocol.Node, presentedToken string) (protocol.Node, string, error) {
	q := h.store.Queries()

	hash, err := h.storedTokenHash(ctx, existing.ID)
	if err != nil {
		return protocol.Node{}, "", err
	}
	issued := ""
	if hash != "" {
		if !CheckToken(presentedToken, hash) {
			return protocol.Node{}, "", errors.New("hub: register: invalid token")
		}
	} else {
		// Known node that never received a token: issue one now.
		token, _, err := h.issueToken(ctx, existing.ID)
		if err != nil {
			return protocol.Node{}, "", err
		}
		issued = token
	}
	if err := q.UpdateNodeStatus(ctx, existing.ID, protocol.NodeOnline, time.Now().UTC()); err != nil {
		return protocol.Node{}, "", err
	}
	existing.Status = protocol.NodeOnline
	return existing, issued, nil
}

// issueToken generates a token, stores its hash on the node, and returns the
// plaintext token (returned to the node once).
func (h *Hub) issueToken(ctx context.Context, nodeID string) (string, string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", "", err
	}
	if err := h.store.Queries().UpdateNodeTokenHash(ctx, nodeID, hash); err != nil {
		return "", "", err
	}
	return token, hash, nil
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
	now := time.Now().UTC()
	for _, s := range sessions {
		if s.Status == protocol.SessionDone || s.Status == protocol.SessionFailed {
			continue
		}
		ended := now
		if err := q.UpdateSessionStatus(ctx, s.ID, protocol.SessionFailed, &ended); err != nil {
			h.log.Warn("fail session on node offline", "session_id", s.ID, "err", err)
			continue
		}
		h.bus.Publish(protocol.Event{
			Type: protocol.EvSessionStateChanged,
			Data: protocol.SessionStateChanged{SessionID: s.ID, Status: protocol.SessionFailed},
		})

		task, err := q.GetTaskBySessionID(ctx, s.ID)
		if err != nil {
			continue
		}
		newStatus := taskStatusFor(protocol.SessionFailed)
		if task.Status == newStatus {
			continue
		}
		updated, err := q.UpdateTaskStatus(ctx, task.ID, newStatus)
		if err != nil {
			h.log.Warn("complete task on node offline", "task_id", task.ID, "err", err)
			continue
		}
		h.invalidateTasks(ctx, task.WorkspaceID)
		h.bus.Publish(protocol.Event{
			Type: protocol.EvTaskStatusChanged,
			Data: protocol.TaskStatusChanged{TaskID: updated.ID, From: task.Status, To: updated.Status},
		})
	}
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

	env := protocol.NewEnvelope(protocol.MsgTaskStart, protocol.TaskStartCommand{
		TaskID:        task.ID,
		SessionID:     session.ID,
		AgentType:     task.AgentType,
		Prompt:        task.Prompt,
		WorkspacePath: workspace.Path,
	})
	if !h.sendToNode(workspace.NodeID, env) {
		h.log.Warn("could not deliver task.start to node", "task_id", task.ID, "node_id", workspace.NodeID)
	}
	return updated, nil
}
