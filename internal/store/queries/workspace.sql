-- name: CreateWorkspace :one
INSERT INTO workspace (node_id, name, path)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspace WHERE id = $1;

-- name: ListWorkspacesByNode :many
SELECT * FROM workspace WHERE node_id = $1 ORDER BY created_at;

-- name: UpdateWorkspaceAutoAllowScope :one
UPDATE workspace SET auto_allow_scope = $2 WHERE id = $1 RETURNING *;
