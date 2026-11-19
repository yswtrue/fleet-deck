-- name: CreateWorkspace :one
INSERT INTO workspace (node_id, name, path)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspace WHERE id = $1;

-- name: ListWorkspacesByNode :many
SELECT * FROM workspace WHERE node_id = $1 ORDER BY created_at;

-- name: UpdateWorkspaceName :one
UPDATE workspace SET name = $2 WHERE id = $1 RETURNING *;

-- name: UpdateWorkspaceAutoAllowScope :one
UPDATE workspace SET auto_allow_scope = $2 WHERE id = $1 RETURNING *;

-- name: DeleteWorkspace :exec
DELETE FROM workspace WHERE id = $1;

-- name: CountActiveSessionsByWorkspace :one
SELECT count(*) FROM session s
JOIN task t ON t.id = s.task_id
WHERE t.workspace_id = $1 AND s.status NOT IN ('done', 'failed');
-- Favorites: per-user bookmarks for quick navigation. The list endpoint
-- filters by the workspace node visibility, so a favorite cannot leak a
-- workspace the user is no longer bound to.

-- name: ListFavoriteWorkspaces :many
SELECT w.* FROM workspace_favorite f
JOIN workspace w ON w.id = f.workspace_id
WHERE f.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(is_admin)::boolean OR EXISTS (
      SELECT 1 FROM node_members m
      WHERE m.user_id = f.user_id AND m.node_id = w.node_id
  ))
ORDER BY f.created_at DESC, w.name;

-- name: AddWorkspaceFavorite :exec
INSERT INTO workspace_favorite (user_id, workspace_id)
VALUES ($1, $2)
ON CONFLICT (user_id, workspace_id) DO NOTHING;

-- name: RemoveWorkspaceFavorite :exec
DELETE FROM workspace_favorite
WHERE user_id = $1 AND workspace_id = $2;

