-- name: CreateTask :one
INSERT INTO task (workspace_id, title, prompt, status, agent_type, sort_order, auto_start)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTask :one
SELECT * FROM task WHERE id = $1;

-- name: ListTasksByWorkspace :many
SELECT * FROM task WHERE workspace_id = $1 AND archived_at IS NULL ORDER BY sort_order, created_at;

-- name: ListArchivedTasksByWorkspace :many
SELECT * FROM task WHERE workspace_id = $1 AND archived_at IS NOT NULL ORDER BY sort_order, created_at;

-- name: ListTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1 AND t.archived_at IS NULL
ORDER BY t.sort_order, t.created_at;

-- name: ListArchivedTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1 AND t.archived_at IS NOT NULL
ORDER BY t.sort_order, t.created_at;

-- name: SearchTasksByWorkspace :many
SELECT * FROM task
WHERE workspace_id = $1
  AND archived_at IS NULL
  AND (title ILIKE '%' || sqlc.arg('q') || '%' OR title % sqlc.arg('q'))
ORDER BY similarity(title, sqlc.arg('q')) DESC, sort_order, created_at;

-- name: SearchArchivedTasksByWorkspace :many
SELECT * FROM task
WHERE workspace_id = $1
  AND archived_at IS NOT NULL
  AND (title ILIKE '%' || sqlc.arg('q') || '%' OR title % sqlc.arg('q'))
ORDER BY similarity(title, sqlc.arg('q')) DESC, sort_order, created_at;

-- name: SearchTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
  AND t.archived_at IS NULL
  AND (t.title ILIKE '%' || sqlc.arg('q') || '%' OR t.title % sqlc.arg('q'))
ORDER BY similarity(t.title, sqlc.arg('q')) DESC, t.sort_order, t.created_at;

-- name: SearchArchivedTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
  AND t.archived_at IS NOT NULL
  AND (t.title ILIKE '%' || sqlc.arg('q') || '%' OR t.title % sqlc.arg('q'))
ORDER BY similarity(t.title, sqlc.arg('q')) DESC, t.sort_order, t.created_at;

-- name: UpdateTaskStatus :one
UPDATE task
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateTaskStatusManual :one
UPDATE task
SET status = $2,
    cancelled_at = CASE
        WHEN status IN ('running', 'awaiting_user') AND $2 = 'done'
        THEN COALESCE(cancelled_at, now())
        ELSE cancelled_at
    END,
    updated_at = now()
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

-- name: UpdateTaskArchivedAt :one
UPDATE task
SET archived_at = sqlc.narg('archived_at'), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AggregateTaskStats :one
SELECT
    COUNT(*) FILTER (
        WHERE (sqlc.narg('from')::timestamptz IS NULL OR t.created_at >= sqlc.narg('from'))
          AND (sqlc.narg('to')::timestamptz IS NULL OR t.created_at <= sqlc.narg('to'))
    )::bigint AS created,
    COUNT(*) FILTER (
        WHERE t.status = 'done'
          AND (sqlc.narg('from')::timestamptz IS NULL OR t.updated_at >= sqlc.narg('from'))
          AND (sqlc.narg('to')::timestamptz IS NULL OR t.updated_at <= sqlc.narg('to'))
    )::bigint AS completed
FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE (sqlc.narg('workspace_id')::uuid IS NULL OR t.workspace_id = sqlc.narg('workspace_id'))
  AND (sqlc.narg('node_id')::uuid IS NULL OR w.node_id = sqlc.narg('node_id'))
  AND (sqlc.narg('member_user_id')::uuid IS NULL OR EXISTS (
        SELECT 1 FROM node_members nm
        WHERE nm.user_id = sqlc.narg('member_user_id') AND nm.node_id = w.node_id));

-- name: AggregateTaskStatsByStatus :many
SELECT
    t.status,
    COUNT(*)::bigint AS task_count
FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE (sqlc.narg('from')::timestamptz IS NULL OR t.created_at >= sqlc.narg('from'))
  AND (sqlc.narg('to')::timestamptz IS NULL OR t.created_at <= sqlc.narg('to'))
  AND (sqlc.narg('workspace_id')::uuid IS NULL OR t.workspace_id = sqlc.narg('workspace_id'))
  AND (sqlc.narg('node_id')::uuid IS NULL OR w.node_id = sqlc.narg('node_id'))
  AND (sqlc.narg('member_user_id')::uuid IS NULL OR EXISTS (
        SELECT 1 FROM node_members nm
        WHERE nm.user_id = sqlc.narg('member_user_id') AND nm.node_id = w.node_id))
GROUP BY t.status
ORDER BY t.status;
