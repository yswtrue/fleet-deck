package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
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

var errWorkspaceNotFound = errors.New("hub: workspace not found")

// canReadWorkspace reports whether user may observe workspaceID: admins see
// everything, non-admins only memberships. It returns errWorkspaceNotFound
// when the workspace does not exist, so callers can render 404 either way
// (a non-member must not learn whether an invisible workspace exists).
func (h *Hub) canReadWorkspace(ctx context.Context, user *store.User, workspaceID string) (bool, error) {
	if _, err := h.store.Queries().GetWorkspace(ctx, workspaceID); err != nil {
		return false, errWorkspaceNotFound
	}
	if user.Role == "admin" {
		return true, nil
	}
	member, err := h.store.Queries().IsWorkspaceMember(ctx, workspaceID, user.ID)
	if err != nil {
		return false, err
	}
	return member, nil
}

// workspaceHandler is a handler scoped to one visible workspace: the {id}
// path value resolved by requireWorkspaceMember.
type workspaceHandler func(http.ResponseWriter, *http.Request, string)

// requireWorkspaceMember resolves the {id} workspace and enforces visibility:
// missing or invisible workspaces both render 404. On success it calls next
// with the workspace id. Use it for every route scoped to one workspace.
func (h *Hub) requireWorkspaceMember(next workspaceHandler) http.HandlerFunc {
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

// canWriteWorkspace reports whether user may mutate inside workspaceID:
// admins everywhere, non-admins only in member workspaces. A missing
// workspace is not writable by anyone.
func (h *Hub) canWriteWorkspace(ctx context.Context, user *store.User, workspaceID string) (bool, error) {
	ok, err := h.canReadWorkspace(ctx, user, workspaceID)
	if err != nil || !ok {
		return false, err
	}
	return true, nil
}

// taskVisibleTo resolves a task for user, enforcing membership of the task's
// workspace. Invisible or missing tasks both surface as an error so callers
// render 404 without leaking existence.
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

// sessionVisibleTo resolves a session for user, enforcing membership of the
// workspace that owns the session's task. It is the single gate for every
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
