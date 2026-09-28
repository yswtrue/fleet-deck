package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// authTokenTTL is how long a freshly minted bearer token stays valid.
const authTokenTTL = 30 * 24 * time.Hour

// userContextKey carries the authenticated *store.User through the request.
type userContextKey struct{}

// tokenHashContextKey carries the sha256 hex of the presented bearer token, so
// logout can revoke exactly the presented token.
type tokenHashContextKey struct{}

// hashAuthToken returns the sha256 hex of an opaque bearer token. Bearer
// tokens carry 256 bits of entropy, so a deterministic saltless hash is both
// safe to store and directly queryable (unlike bcrypt-scoped node secrets).
func hashAuthToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mintAuthToken issues an opaque 32-byte base64url bearer token for userID,
// stores its hash, and returns the plaintext token (exactly once) plus the
// stored row.
func (h *Hub) mintAuthToken(ctx context.Context, userID string) (token string, row store.AuthToken, err error) {
	token, err = GenerateToken()
	if err != nil {
		return "", store.AuthToken{}, err
	}
	row, err = h.store.Queries().CreateAuthToken(ctx, store.CreateAuthTokenParams{
		UserID:    userID,
		TokenHash: hashAuthToken(token),
		ExpiresAt: time.Now().UTC().Add(authTokenTTL),
	})
	if err != nil {
		return "", store.AuthToken{}, err
	}
	return token, row, nil
}

// userFromRequest resolves the Authorization: Bearer token to its user. A nil
// user (with no error) means no usable credential was presented.
func (h *Hub) userFromRequest(r *http.Request) (*store.User, string, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, "", nil
	}
	return h.userFromToken(r.Context(), token)
}

// userFromToken resolves an opaque token string to its user, enforcing
// expiry and revocation. Unknown, expired, or revoked tokens yield a nil user.
// The resolution is cached (authCacheTTL) since every authenticated request
// and WebSocket handshake goes through here; logout deletes the entry
// explicitly and the TTL bounds any other missed revocation.
func (h *Hub) userFromToken(ctx context.Context, token string) (*store.User, string, error) {
	if token == "" {
		return nil, "", nil
	}
	hash := hashAuthToken(token)
	cacheKey := "auth:token:" + hash
	var cached authCacheEntry
	if h.cachedJSON(ctx, cacheKey, &cached) {
		if !time.Now().UTC().Before(cached.ExpiresAt) {
			return nil, "", nil // cached but past the token's own expiry
		}
		user := cached.User
		return &user, hash, nil
	}
	row, err := h.store.Queries().GetAuthTokenByHash(ctx, hash)
	if err != nil {
		return nil, "", nil // unknown token: unauthenticated, not a server error
	}
	if row.RevokedAt != nil || !time.Now().UTC().Before(row.ExpiresAt) {
		return nil, "", nil
	}
	user, err := h.store.Queries().GetUser(ctx, row.UserID)
	if err != nil {
		return nil, "", nil // owner deleted: behave as unauthenticated
	}
	h.cacheSet(ctx, cacheKey, authCacheEntry{User: user, ExpiresAt: row.ExpiresAt}, authCacheTTL)
	return &user, hash, nil
}

// bearerToken extracts the token from an Authorization: Bearer header.
func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

// userOf returns the authenticated user attached by requireAuth, or nil.
func userOf(r *http.Request) *store.User {
	u, _ := r.Context().Value(userContextKey{}).(*store.User)
	return u
}

// tokenHashOf returns the sha256 hex of the request's presented token. It is
// set by requireAuth alongside the user.
func tokenHashOf(r *http.Request) string {
	hash, _ := r.Context().Value(tokenHashContextKey{}).(string)
	return hash
}

// withAuth attaches the request's bearer user (if any) to the context before
// calling next. It never rejects.
func (h *Hub) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, hash, _ := h.userFromRequest(r)
		if user != nil {
			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			ctx = context.WithValue(ctx, tokenHashContextKey{}, hash)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

// requireAuth rejects requests without a valid bearer token with 401
// {"error":"unauthorized"} and passes user-bearing requests to next.
func (h *Hub) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return h.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if userOf(r) == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	})
}

// requireAdmin additionally rejects authenticated non-admin users with 403.
func (h *Hub) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if userOf(r).Role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	})
}

var (
	errNodeNotFound      = errors.New("hub: node not found")
	errWorkspaceNotFound = errors.New("hub: workspace not found")
)

// nodeVisibleTo resolves a node for user, enforcing the user<->node binding:
// admins see every node, non-admins only their bound nodes. Missing or
// unbound nodes both surface as an error so callers render 404 without
// leaking existence.
func (h *Hub) nodeVisibleTo(ctx context.Context, user *store.User, nodeID string) (protocol.Node, error) {
	node, err := h.store.Queries().GetNode(ctx, nodeID)
	if err != nil {
		return protocol.Node{}, err
	}
	if user.Role == "admin" {
		return node, nil
	}
	bound, err := h.store.Queries().IsNodeMember(ctx, user.ID, nodeID)
	if err != nil {
		return protocol.Node{}, err
	}
	if !bound {
		return protocol.Node{}, errNodeNotFound
	}
	return node, nil
}

// canReadNode reports whether user may access nodeID's subtree (read and
// write; a node binding grants both). It returns an error when the node does
// not exist, so callers can render 404 either way (an unbound user must not
// learn whether an invisible node exists).
func (h *Hub) canReadNode(ctx context.Context, user *store.User, nodeID string) (bool, error) {
	if _, err := h.nodeVisibleTo(ctx, user, nodeID); err != nil {
		return false, err
	}
	return true, nil
}

// canReadWorkspace reports whether user may observe workspaceID: the
// workspace resolves to its node, and visibility follows the binding on that
// node (subtree inheritance). It returns errWorkspaceNotFound when the
// workspace does not exist, so callers can render 404 either way.
func (h *Hub) canReadWorkspace(ctx context.Context, user *store.User, workspaceID string) (bool, error) {
	workspace, err := h.store.Queries().GetWorkspace(ctx, workspaceID)
	if err != nil {
		return false, errWorkspaceNotFound
	}
	return h.canReadNode(ctx, user, workspace.NodeID)
}

// workspaceHandler is a handler scoped to one visible workspace: the {id}
// path value resolved by requireWorkspaceAccess.
type workspaceHandler func(http.ResponseWriter, *http.Request, string)

// requireWorkspaceAccess resolves the {id} workspace and enforces
// visibility: missing or invisible workspaces both render 404. On success it
// calls next with the workspace id. Use it for every route scoped to one
// workspace.
func (h *Hub) requireWorkspaceAccess(next workspaceHandler) http.HandlerFunc {
	return h.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ok, err := h.canReadWorkspace(r.Context(), userOf(r), id)
		if err != nil || !ok {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		next(w, r, id)
	})
}

// canWriteWorkspace reports whether user may mutate inside workspaceID. The
// binding model has no read-only grants -- a node binding is read+write
// across the subtree -- so this is exactly canReadWorkspace. A missing
// workspace is not writable by anyone.
func (h *Hub) canWriteWorkspace(ctx context.Context, user *store.User, workspaceID string) (bool, error) {
	return h.canReadWorkspace(ctx, user, workspaceID)
}

// ---- WebSocket broadcast scoping ------------------------------------------

// nodeScopeTTL bounds how long a cached user->bound-node set is trusted before
// it is re-read from Postgres. Explicit binding rewrites invalidate the entry
// outright (see invalidateNodeBindings); the TTL only bounds staleness from
// out-of-band row changes.
const nodeScopeTTL = 15 * time.Second

// bindingEntry is one cached user -> bound-node-id set.
type bindingEntry struct {
	nodes   map[string]bool
	expires time.Time
}

// bindingCache caches which nodes each user is bound to, so per-event
// visibility checks during WebSocket fan-out do not hit Postgres per event per
// subscriber (streaming deltas arrive in bursts).
type bindingCache struct {
	mu      sync.Mutex
	entries map[string]bindingEntry
}

func newBindingCache() *bindingCache {
	return &bindingCache{entries: make(map[string]bindingEntry)}
}

// nodes returns userID's bound-node set, re-reading it via load when the entry
// is missing or stale.
func (c *bindingCache) nodes(userID string, load func() (map[string]bool, error)) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[userID]; ok && time.Now().Before(e.expires) {
		return e.nodes, nil
	}
	bound, err := load()
	if err != nil {
		return nil, err
	}
	c.entries[userID] = bindingEntry{nodes: bound, expires: time.Now().Add(nodeScopeTTL)}
	return bound, nil
}

// invalidate drops userID's cached bindings so a binding rewrite takes effect
// on the very next event.
func (c *bindingCache) invalidate(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, userID)
}

// invalidateNodeBindings drops a user's cached bound-node set. Every write to
// node_members must call it so unbinds stop deliveries immediately.
func (h *Hub) invalidateNodeBindings(userID string) {
	h.bindings.invalidate(userID)
}

// boundNodes returns the set of node ids userID is bound to, through the
// short-TTL cache.
func (h *Hub) boundNodes(userID string) (map[string]bool, error) {
	return h.bindings.nodes(userID, func() (map[string]bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nodes, err := h.store.Queries().ListNodesByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			set[n.ID] = true
		}
		return set, nil
	})
}

// webEventScope returns the broadcast scope for one /ws/web connection: admins
// receive every event, other users only events inside their bound nodes'
// subtrees. Events whose owning node cannot be resolved are dropped for
// non-admins (no leak by accident).
func (h *Hub) webEventScope(user *store.User) EventScope {
	if user.Role == "admin" {
		return EventScope{All: true}
	}
	// Pre-warm the binding cache so the first events after connect do not
	// serialize on a cold load behind the broadcaster lock.
	_, _ = h.boundNodes(user.ID)
	return EventScope{Visible: func(nodeID string) bool {
		bound, err := h.boundNodes(user.ID)
		if err != nil {
			return false // cache/DB failure: drop rather than leak
		}
		return bound[nodeID]
	}}
}

// taskVisibleTo resolves a task for user, enforcing the node binding of the
// task's workspace. Invisible or missing tasks both surface as an error so
// callers render 404 without leaking existence.
func (h *Hub) taskVisibleTo(ctx context.Context, user *store.User, taskID string) (protocol.Task, error) {
	task, err := h.store.Queries().GetTask(ctx, taskID)
	if err != nil {
		return protocol.Task{}, err
	}
	ok, verr := h.canReadWorkspace(ctx, user, task.WorkspaceID)
	if verr != nil || !ok {
		return protocol.Task{}, errWorkspaceNotFound
	}
	return task, nil
}

// sessionVisibleTo resolves a session for user, enforcing the node binding of
// the workspace that owns the session's task. It is the single gate for every
// session-scoped route (snapshot, messages, events, send).
func (h *Hub) sessionVisibleTo(ctx context.Context, user *store.User, sessionID string) (protocol.Session, error) {
	session, err := h.store.Queries().GetSession(ctx, sessionID)
	if err != nil {
		return protocol.Session{}, err
	}
	task, err := h.store.Queries().GetTask(ctx, session.TaskID)
	if err != nil {
		// An orphaned session (parent task unresolvable) is visible to
		// admins -- it carries no workspace to scope by -- and invisible to
		// everyone else. GET /api/sessions/{id} renders task as null.
		if user.Role != "admin" {
			return protocol.Session{}, errWorkspaceNotFound
		}
		return session, nil
	}
	ok, verr := h.canReadWorkspace(ctx, user, task.WorkspaceID)
	if verr != nil || !ok {
		return protocol.Session{}, errWorkspaceNotFound
	}
	return session, nil
}
