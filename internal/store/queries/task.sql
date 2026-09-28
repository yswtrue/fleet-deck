-- name: CreateTask :one
INSERT INTO task (workspace_id, title, prompt, status, agent_type, sort_order)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTask :one
SELECT * FROM task WHERE id = $1;

-- name: ListTasksByWorkspace :many
SELECT * FROM task WHERE workspace_id = $1 ORDER BY sort_order, created_at;

-- name: ListTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
ORDER BY t.sort_order, t.created_at;

-- name: SearchTasksByWorkspace :many
SELECT * FROM task
WHERE workspace_id = $1
  AND (title ILIKE '%' || sqlc.arg('q') || '%' OR title % sqlc.arg('q'))
ORDER BY similarity(title, sqlc.arg('q')) DESC, sort_order, created_at;

-- name: SearchTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
  AND (t.title ILIKE '%' || sqlc.arg('q') || '%' OR t.title % sqlc.arg('q'))
ORDER BY similarity(t.title, sqlc.arg('q')) DESC, t.sort_order, t.created_at;

-- name: UpdateTaskStatus :one
UPDATE task
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateTaskSessionID :exec
UPDATE task
SET session_id = sqlc.narg('session_id'), updated_at = now()
WHERE id = $1;

-- name: GetTaskBySessionID :one
SELECT * FROM task WHERE session_id = $1;

-- name: UpdateTaskSortOrder :exec
UPDATE task
SET sort_order = $2, updated_at = now()
WHERE id = $1;
