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

// ---- workspace members -----------------------------------------------------

// handleListWorkspaceMembers returns a workspace's member users. The route is
// gated by requireWorkspaceMember: admins and members only.
func (h *Hub) handleListWorkspaceMembers(w http.ResponseWriter, r *http.Request, workspaceID string) {
	members, err := h.store.Queries().ListWorkspaceMembers(r.Context(), workspaceID)
	if err != nil {
		h.serverError(w, "list workspace members", err)
		return
	}
	writeJSON(w, http.StatusOK, usersToDTO(members))
}

// handleAddWorkspaceMember grants a workspace to a user (admin only; the
// caller already passed requireWorkspaceMember). Idempotent: re-adding a
// member returns 201 with that member.
func (h *Hub) handleAddWorkspaceMember(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if userOf(r).Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	ctx := r.Context()
	user, err := h.store.Queries().GetUser(ctx, req.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.serverError(w, "get user", err)
		return
	}
	if err := h.store.Queries().AddWorkspaceMember(ctx, workspaceID, user.ID); err != nil {
		h.serverError(w, "add workspace member", err)
		return
	}
	writeJSON(w, http.StatusCreated, userToDTO(user))
}

// handleRemoveWorkspaceMember revokes a workspace grant (admin only).
func (h *Hub) handleRemoveWorkspaceMember(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if userOf(r).Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	memberID := r.PathValue("user_id")
	if err := h.store.Queries().RemoveWorkspaceMember(r.Context(), workspaceID, memberID); err != nil {
		h.serverError(w, "remove workspace member", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), e.g. a duplicate email on user creation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
