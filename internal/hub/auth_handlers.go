package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"golang.org/x/crypto/bcrypt"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// userDTO is the wire shape of a user: id, email, display_name, role,
// created_at. The password hash never leaves the store layer.
type userDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func userToDTO(u store.User) userDTO {
	return userDTO{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
	}
}

func usersToDTO(users []store.User) []userDTO {
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, userToDTO(u))
	}
	return out
}

// normalizeEmail lowercases and trims an email address for storage and
// lookup, so " User@Example.com " and "user@example.com" are one identity.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validNewPassword reports whether a plaintext password meets the minimum
// length policy for create and reset.
func validNewPassword(password string) bool {
	return len(password) >= 8
}

// hashPassword bcrypt-hashes a plaintext password.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// tokenResponse is the body of setup/login: the opaque token (returned exactly
// once), its expiry, and the authenticated user.
type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      userDTO   `json:"user"`
}

// ---- setup / login ---------------------------------------------------------

// handleAuthStatus reports whether the hub still needs its first user.
// It is open (no auth) so a fresh web client can route to the setup screen.
func (h *Hub) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	n, err := h.store.Queries().CountUsers(r.Context())
	if err != nil {
		h.serverError(w, "auth status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": n == 0})
}

// handleAuthSetup creates the first user (always an admin) and returns a
// bearer token. It only runs while the users table is empty; afterwards it
// is 409 so the open endpoint cannot mint further accounts.
func (h *Hub) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := normalizeEmail(req.Email)
	if email == "" || strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "email and display_name are required")
		return
	}
	if !validNewPassword(req.Password) {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	n, err := h.store.Queries().CountUsers(ctx)
	if err != nil {
		h.serverError(w, "auth setup", err)
		return
	}
	if n != 0 {
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		h.serverError(w, "auth setup", err)
		return
	}
	user, err := h.store.Queries().CreateUser(ctx, store.CreateUserParams{
		Email:        email,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: hash,
		Role:         "admin",
	})
	if err != nil {
		h.serverError(w, "auth setup", err)
		return
	}
	token, row, err := h.mintAuthToken(ctx, user.ID)
	if err != nil {
		h.serverError(w, "auth setup", err)
		return
	}
	writeJSON(w, http.StatusCreated, tokenResponse{Token: token, ExpiresAt: row.ExpiresAt, User: userToDTO(user)})
}

// handleAuthLogin verifies an email+password pair and returns a fresh bearer
// token. Unknown emails and wrong passwords both yield the same 401 so the
// endpoint cannot be used to enumerate accounts.
func (h *Hub) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx := r.Context()
	user, err := h.store.Queries().GetUserByEmail(ctx, normalizeEmail(req.Email))
	if err == nil {
		err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
	}
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && err != bcrypt.ErrMismatchedHashAndPassword {
			h.log.Warn("login lookup failed", "err", err)
		}
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	token, row, err := h.mintAuthToken(ctx, user.ID)
	if err != nil {
		h.serverError(w, "auth login", err)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: token, ExpiresAt: row.ExpiresAt, User: userToDTO(user)})
}

// handleAuthLogout revokes exactly the presented bearer token. The token is
// unusable from this point on (reuse renders 401).
func (h *Hub) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Queries().RevokeAuthToken(r.Context(), tokenHashOf(r)); err != nil {
		h.serverError(w, "auth logout", err)
		return
	}
	// Drop the cached token resolution so the revocation takes effect at once.
	h.cacheDelete(r.Context(), "auth:token:"+tokenHashOf(r))
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthMe returns the authenticated user.
func (h *Hub) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userToDTO(*userOf(r)))
}

// ---- user admin ------------------------------------------------------------

// handleListUsers returns every user (admin only).
func (h *Hub) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.Queries().ListUsers(r.Context())
	if err != nil {
		h.serverError(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, usersToDTO(users))
}

// handleCreateUser creates a user with an explicit role (admin only).
func (h *Hub) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := normalizeEmail(req.Email)
	if email == "" || strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "email and display_name are required")
		return
	}
	if req.Role != "admin" && req.Role != "user" {
		writeError(w, http.StatusBadRequest, "role must be admin or user")
		return
	}
	if !validNewPassword(req.Password) {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		h.serverError(w, "create user", err)
		return
	}
	user, err := h.store.Queries().CreateUser(r.Context(), store.CreateUserParams{
		Email:        email,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: hash,
		Role:         req.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email already in use")
			return
		}
		h.serverError(w, "create user", err)
		return
	}
	writeJSON(w, http.StatusCreated, userToDTO(user))
}

// handlePatchUser updates display_name, role, and/or password (admin only).
// Demoting or deleting the last admin is refused with 409.
func (h *Hub) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Password    *string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx := r.Context()
	user, err := h.store.Queries().GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.serverError(w, "get user", err)
		return
	}
	displayName := user.DisplayName
	if req.DisplayName != nil {
		if strings.TrimSpace(*req.DisplayName) == "" {
			writeError(w, http.StatusBadRequest, "display_name must not be empty")
			return
		}
		displayName = strings.TrimSpace(*req.DisplayName)
	}

	role := user.Role
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "user" {
			writeError(w, http.StatusBadRequest, "role must be admin or user")
			return
		}
		role = *req.Role
	}

	passwordHash := ""
	if req.Password != nil {
		if !validNewPassword(*req.Password) {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		hash, err := hashPassword(*req.Password)
		if err != nil {
			h.serverError(w, "patch user", err)
			return
		}
		passwordHash = hash
	}

	// Never allow removing the last admin, whether by demotion or (below) by
	// deletion.
	if user.Role == "admin" && role != "admin" {
		n, err := h.store.Queries().CountAdmins(ctx)
		if err != nil {
			h.serverError(w, "patch user", err)
			return
		}
		if n <= 1 {
			writeError(w, http.StatusConflict, "cannot demote the last admin")
			return
		}
	}

	updated, err := h.store.Queries().UpdateUser(ctx, store.UpdateUserParams{
		ID:          id,
		DisplayName: displayName,
		Role:        role,
	})
	if err != nil {
		h.serverError(w, "patch user", err)
		return
	}
	if passwordHash != "" {
		if err := h.store.Queries().UpdateUserPasswordHash(ctx, id, passwordHash); err != nil {
			h.serverError(w, "patch user", err)
			return
		}
		// A password reset ends every live session of that account.
		h.revokeAllTokens(ctx, id)
	}
	writeJSON(w, http.StatusOK, userToDTO(updated))
}

// handleDeleteUser removes a user (admin only). Deleting yourself or the last
// admin is refused with 409.
func (h *Hub) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == userOf(r).ID {
		writeError(w, http.StatusConflict, "cannot delete yourself")
		return
	}

	ctx := r.Context()
	user, err := h.store.Queries().GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.serverError(w, "get user", err)
		return
	}
	if user.Role == "admin" {
		n, err := h.store.Queries().CountAdmins(ctx)
		if err != nil {
			h.serverError(w, "delete user", err)
			return
		}
		if n <= 1 {
			writeError(w, http.StatusConflict, "cannot delete the last admin")
			return
		}
	}
	if err := h.store.Queries().DeleteUser(ctx, id); err != nil {
		h.serverError(w, "delete user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) revokeAllTokens(ctx context.Context, userID string) {
	if err := h.store.Queries().RevokeAuthTokensByUser(ctx, userID); err != nil {
		h.log.Warn("revoke user tokens", "user_id", userID, "err", err)
	}
}

// ---- user node bindings ----------------------------------------------------

// handleGetUserNodes returns the nodes a user is bound to (admin only). It is
// the read side of the binding management plane.
func (h *Hub) handleGetUserNodes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if _, err := h.store.Queries().GetUser(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.serverError(w, "get user", err)
		return
	}
	nodes, err := h.store.Queries().ListNodesByUser(ctx, id)
	if err != nil {
		h.serverError(w, "list user nodes", err)
		return
	}
	if nodes == nil {
		nodes = []protocol.Node{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// handleSetUserNodes replaces a user's full node binding set (admin only):
// the body {"node_ids": [...]} is the complete new set, so passing a subset
// unbinds everything else. Idempotent: PUTting the unchanged set is a no-op
// and answers 200 with the resulting nodes.
func (h *Hub) handleSetUserNodes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NodeIDs == nil {
		writeError(w, http.StatusBadRequest, "node_ids is required")
		return
	}

	ctx := r.Context()
	if _, err := h.store.Queries().GetUser(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.serverError(w, "get user", err)
		return
	}

	// De-duplicate the requested set and verify every node exists up front,
	// so the replace cannot half-apply or fail on a foreign key.
	want := make(map[string]bool, len(req.NodeIDs))
	ids := make([]string, 0, len(req.NodeIDs))
	for _, nodeID := range req.NodeIDs {
		if want[nodeID] {
			continue
		}
		want[nodeID] = true
		ids = append(ids, nodeID)
	}
	for _, nodeID := range ids {
		if _, err := h.store.Queries().GetNode(ctx, nodeID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "node not found")
				return
			}
			h.serverError(w, "get node", err)
			return
		}
	}

	// Replace = drop stale bindings, add missing ones, in one transaction.
	if err := h.store.WithTx(ctx, func(q *store.Queries) error {
		have, err := q.ListNodeMembersByUser(ctx, id)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(have))
		for _, m := range have {
			present[m.NodeID] = true
			if !want[m.NodeID] {
				if err := q.RemoveNodeMember(ctx, id, m.NodeID); err != nil {
					return err
				}
			}
		}
		for _, nodeID := range ids {
			if !present[nodeID] {
				if err := q.AddNodeMember(ctx, id, nodeID); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		h.serverError(w, "set user nodes", err)
		return
	}
	// Binding rewrites invalidate the cached bound-node set so /ws/web
	// scoping reflects the new set on the very next event.
	h.invalidateNodeBindings(id)

	nodes, err := h.store.Queries().ListNodesByUser(ctx, id)
	if err != nil {
		h.serverError(w, "list user nodes", err)
		return
	}
	if nodes == nil {
		nodes = []protocol.Node{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), e.g. a duplicate email on user creation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
