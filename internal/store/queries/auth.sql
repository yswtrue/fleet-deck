-- name: CreateUser :one
INSERT INTO users (email, display_name, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at, email;

-- name: CountUsers :one
SELECT COUNT(*)::bigint AS user_count FROM users;

-- name: CountAdmins :one
SELECT COUNT(*)::bigint AS admin_count FROM users WHERE role = 'admin';

-- name: UpdateUser :one
UPDATE users
SET display_name = $2, role = $3
WHERE id = $1
RETURNING *;

-- name: UpdateUserPasswordHash :exec
UPDATE users
SET password_hash = $2
WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
-- name: CreateAuthToken :one
INSERT INTO auth_tokens (user_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAuthTokenByHash :one
SELECT * FROM auth_tokens WHERE token_hash = $1;

-- name: RevokeAuthToken :exec
UPDATE auth_tokens
SET revoked_at = now()
WHERE token_hash = $1;

-- name: RevokeAuthTokensByUser :exec
UPDATE auth_tokens
SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;
-- name: ListWorkspaceMembers :many
SELECT u.* FROM users u
JOIN workspace_members m ON m.user_id = u.id
WHERE m.workspace_id = $1
ORDER BY u.created_at, u.email;

-- name: AddWorkspaceMember :one
INSERT INTO workspace_members (workspace_id, user_id)
VALUES ($1, $2)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET workspace_id = EXCLUDED.workspace_id
RETURNING *;

-- name: RemoveWorkspaceMember :exec
DELETE FROM workspace_members
WHERE workspace_id = $1 AND user_id = $2;

-- name: IsWorkspaceMember :one
SELECT EXISTS(
    SELECT 1 FROM workspace_members
    WHERE workspace_id = $1 AND user_id = $2
)::bool AS is_member;

-- name: ListWorkspacesByMember :many
SELECT w.* FROM workspace w
JOIN workspace_members m ON m.workspace_id = w.id
WHERE m.user_id = $1
ORDER BY w.created_at;
