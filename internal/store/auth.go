package store

import (
	"context"
	"fmt"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// User is one hub operator. PasswordHash never leaves the store layer; the
// hub maps User to its wire DTO (id, email, display_name, role, created_at).
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

func userFromRow(r sqlcgen.User) User {
	return User{
		ID:           r.ID,
		Email:        r.Email,
		DisplayName:  r.DisplayName,
		PasswordHash: r.PasswordHash,
		Role:         r.Role,
		CreatedAt:    r.CreatedAt,
	}
}

func usersFromRows(rows []sqlcgen.User) []User {
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r))
	}
	return out
}

// AuthToken is one stored bearer-token row. The opaque token itself is never
// persisted; TokenHash is the sha256 hex of the token.
type AuthToken struct {
	ID        string
	UserID    string
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

func authTokenFromRow(r sqlcgen.AuthToken) AuthToken {
	return AuthToken{
		ID:        r.ID,
		UserID:    r.UserID,
		TokenHash: r.TokenHash,
		CreatedAt: r.CreatedAt,
		ExpiresAt: r.ExpiresAt,
		RevokedAt: r.RevokedAt,
	}
}

// CreateUserParams are the fields required to register a user. Email must
// already be normalized (lowercase + trimmed) by the caller.
type CreateUserParams struct {
	Email        string
	DisplayName  string
	PasswordHash string
	Role         string
}

// CreateUser inserts a user and returns the stored row.
func (q *Queries) CreateUser(ctx context.Context, p CreateUserParams) (User, error) {
	row, err := q.q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Email:        p.Email,
		DisplayName:  p.DisplayName,
		PasswordHash: p.PasswordHash,
		Role:         p.Role,
	})
	if err != nil {
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	return userFromRow(row), nil
}

// GetUser returns a user by id.
func (q *Queries) GetUser(ctx context.Context, id string) (User, error) {
	row, err := q.q.GetUser(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("store: get user: %w", err)
	}
	return userFromRow(row), nil
}

// GetUserByEmail returns a user by normalized email.
func (q *Queries) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := q.q.GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, fmt.Errorf("store: get user by email: %w", err)
	}
	return userFromRow(row), nil
}

// ListUsers returns all users ordered by creation time.
func (q *Queries) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := q.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	return usersFromRows(rows), nil
}

// CountUsers returns the total number of users.
func (q *Queries) CountUsers(ctx context.Context) (int64, error) {
	n, err := q.q.CountUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of users with role 'admin'.
func (q *Queries) CountAdmins(ctx context.Context) (int64, error) {
	n, err := q.q.CountAdmins(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

// UpdateUserParams are the mutable user fields.
type UpdateUserParams struct {
	ID          string
	DisplayName string
	Role        string
}

// UpdateUser rewrites a user's display name and role.
func (q *Queries) UpdateUser(ctx context.Context, p UpdateUserParams) (User, error) {
	row, err := q.q.UpdateUser(ctx, sqlcgen.UpdateUserParams{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		Role:        p.Role,
	})
	if err != nil {
		return User{}, fmt.Errorf("store: update user: %w", err)
	}
	return userFromRow(row), nil
}

// UpdateUserPasswordHash replaces a user's bcrypt password hash.
func (q *Queries) UpdateUserPasswordHash(ctx context.Context, id, hash string) error {
	if err := q.q.UpdateUserPasswordHash(ctx, sqlcgen.UpdateUserPasswordHashParams{
		ID:           id,
		PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("store: update user password: %w", err)
	}
	return nil
}

// DeleteUser removes a user; auth tokens and memberships cascade.
func (q *Queries) DeleteUser(ctx context.Context, id string) error {
	if err := q.q.DeleteUser(ctx, id); err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	return nil
}

// CreateAuthTokenParams are the fields required to store a bearer token.
// TokenHash must be the sha256 hex of the opaque token; ExpiresAt is the hub's
// token TTL applied at mint time.
type CreateAuthTokenParams struct {
	UserID    string
	TokenHash string
	ExpiresAt time.Time
}

// CreateAuthToken inserts a token row and returns it.
func (q *Queries) CreateAuthToken(ctx context.Context, p CreateAuthTokenParams) (AuthToken, error) {
	row, err := q.q.CreateAuthToken(ctx, sqlcgen.CreateAuthTokenParams{
		UserID:    p.UserID,
		TokenHash: p.TokenHash,
		ExpiresAt: p.ExpiresAt,
	})
	if err != nil {
		return AuthToken{}, fmt.Errorf("store: create auth token: %w", err)
	}
	return authTokenFromRow(row), nil
}

// GetAuthTokenByHash returns the token row for a sha256 hex token hash.
func (q *Queries) GetAuthTokenByHash(ctx context.Context, hash string) (AuthToken, error) {
	row, err := q.q.GetAuthTokenByHash(ctx, hash)
	if err != nil {
		return AuthToken{}, fmt.Errorf("store: get auth token: %w", err)
	}
	return authTokenFromRow(row), nil
}

// RevokeAuthToken marks one token revoked.
func (q *Queries) RevokeAuthToken(ctx context.Context, hash string) error {
	if err := q.q.RevokeAuthToken(ctx, hash); err != nil {
		return fmt.Errorf("store: revoke auth token: %w", err)
	}
	return nil
}

// RevokeAuthTokensByUser marks all of a user's live tokens revoked.
func (q *Queries) RevokeAuthTokensByUser(ctx context.Context, userID string) error {
	if err := q.q.RevokeAuthTokensByUser(ctx, userID); err != nil {
		return fmt.Errorf("store: revoke user auth tokens: %w", err)
	}
	return nil
}

// ListWorkspaceMembers returns a workspace's member users ordered by creation.
func (q *Queries) ListWorkspaceMembers(ctx context.Context, workspaceID string) ([]User, error) {
	rows, err := q.q.ListWorkspaceMembers(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list workspace members: %w", err)
	}
	return usersFromRows(rows), nil
}

// AddWorkspaceMember grants a user access to a workspace. It is idempotent:
// re-adding an existing member is a no-op.
func (q *Queries) AddWorkspaceMember(ctx context.Context, workspaceID, userID string) error {
	if _, err := q.q.AddWorkspaceMember(ctx, sqlcgen.AddWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return fmt.Errorf("store: add workspace member: %w", err)
	}
	return nil
}

// RemoveWorkspaceMember revokes a user's access to a workspace. Removing a
// non-member is a no-op.
func (q *Queries) RemoveWorkspaceMember(ctx context.Context, workspaceID, userID string) error {
	if err := q.q.RemoveWorkspaceMember(ctx, sqlcgen.RemoveWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return fmt.Errorf("store: remove workspace member: %w", err)
	}
	return nil
}

// IsWorkspaceMember reports whether a user is a member of a workspace.
func (q *Queries) IsWorkspaceMember(ctx context.Context, workspaceID, userID string) (bool, error) {
	ok, err := q.q.IsWorkspaceMember(ctx, sqlcgen.IsWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		return false, fmt.Errorf("store: workspace membership: %w", err)
	}
	return ok, nil
}

// ListWorkspacesByMember returns the workspaces a user is a member of.
func (q *Queries) ListWorkspacesByMember(ctx context.Context, userID string) ([]protocol.Workspace, error) {
	rows, err := q.q.ListWorkspacesByMember(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list workspaces by member: %w", err)
	}
	return workspacesFromRows(rows), nil
}
