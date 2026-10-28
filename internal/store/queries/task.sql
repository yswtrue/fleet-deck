-- name: CreateTask :one
INSERT INTO task (workspace_id, title, prompt, status, agent_type, sort_order, auto_start, title_auto)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetTask :one
SELECT * FROM task WHERE id = $1;

-- name: ListTasksByWorkspace :many
SELECT * FROM task WHERE workspace_id = $1 AND archived_at IS NULL ORDER BY updated_at DESC, created_at DESC, id;

-- name: ListArchivedTasksByWorkspace :many
SELECT * FROM task WHERE workspace_id = $1 AND archived_at IS NOT NULL ORDER BY updated_at DESC, created_at DESC, id;

-- name: ListTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1 AND t.archived_at IS NULL
ORDER BY t.updated_at DESC, t.created_at DESC, t.id;

-- name: ListArchivedTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1 AND t.archived_at IS NOT NULL
ORDER BY t.updated_at DESC, t.created_at DESC, t.id;

-- name: SearchTasksByWorkspace :many
SELECT * FROM task
WHERE workspace_id = $1
  AND archived_at IS NULL
  AND (title ILIKE '%' || sqlc.arg('q') || '%' OR title % sqlc.arg('q'))
ORDER BY similarity(title, sqlc.arg('q')) DESC, updated_at DESC, created_at DESC, id;

-- name: SearchArchivedTasksByWorkspace :many
SELECT * FROM task
WHERE workspace_id = $1
  AND archived_at IS NOT NULL
  AND (title ILIKE '%' || sqlc.arg('q') || '%' OR title % sqlc.arg('q'))
ORDER BY similarity(title, sqlc.arg('q')) DESC, updated_at DESC, created_at DESC, id;

-- name: SearchTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
  AND t.archived_at IS NULL
  AND (t.title ILIKE '%' || sqlc.arg('q') || '%' OR t.title % sqlc.arg('q'))
ORDER BY similarity(t.title, sqlc.arg('q')) DESC, t.updated_at DESC, t.created_at DESC, t.id;

-- name: SearchArchivedTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1
  AND t.archived_at IS NOT NULL
  AND (t.title ILIKE '%' || sqlc.arg('q') || '%' OR t.title % sqlc.arg('q'))
ORDER BY similarity(t.title, sqlc.arg('q')) DESC, t.updated_at DESC, t.created_at DESC, t.id;

-- name: UpdateTaskStatus :one
UPDATE task
SET status = $2,
    queued_at = CASE WHEN $2::text = 'running' THEN NULL ELSE queued_at END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateTaskStatusManual :one
UPDATE task
SET status = $2,
    queued_at = CASE WHEN $2::text = 'running' THEN NULL ELSE queued_at END,
    cancelled_at = CASE
        WHEN status IN ('running', 'awaiting_user') AND $2 = 'done'
        THEN COALESCE(cancelled_at, now())
        ELSE cancelled_at
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetTaskForUpdate :one
SELECT * FROM task WHERE id = $1 FOR UPDATE;

-- name: CountActiveTasksByWorkspace :one
-- A task occupies the workspace while the board says it is running or waiting
-- for the operator, and also while any of its sessions is still live (a manual
-- running -> waiting move must not start a second agent in the same tree).
SELECT count(*)::bigint FROM task t
WHERE t.workspace_id = $1
  AND (
    t.status IN ('running', 'awaiting_user')
    OR EXISTS (
      SELECT 1 FROM session s
      WHERE s.task_id = t.id AND s.status NOT IN ('done', 'failed')
    )
  );

-- name: MarkTaskQueued :one
UPDATE task
SET queued_at = COALESCE(queued_at, now()), updated_at = now()
WHERE id = $1
  AND archived_at IS NULL
  AND session_id IS NULL
  AND status = 'waiting'
RETURNING *;

-- name: NextQueuedTaskByWorkspace :one
SELECT * FROM task
WHERE workspace_id = $1
  AND queued_at IS NOT NULL
  AND archived_at IS NULL
  AND session_id IS NULL
  AND status = 'waiting'
ORDER BY queued_at, created_at, id
LIMIT 1;

-- name: UpdateTaskSessionID :exec
UPDATE task
SET session_id = sqlc.narg('session_id'), updated_at = now()
WHERE id = $1;

-- name: ScheduleTaskRetry :one
UPDATE task
SET retry_pending = true,
    retry_count = retry_count + 1,
    updated_at = now()
WHERE id = sqlc.arg('id') AND retry_count < sqlc.arg('max_retries')
RETURNING *;

-- name: ListRetryPendingTasksByNode :many
SELECT t.* FROM task t
JOIN workspace w ON w.id = t.workspace_id
WHERE w.node_id = $1 AND t.retry_pending
ORDER BY t.updated_at, t.id;

-- name: ClaimTaskRetry :one
UPDATE task
SET retry_pending = false, updated_at = now()
WHERE id = $1 AND retry_pending
RETURNING *;

-- name: CancelTaskRetry :one
UPDATE task
SET retry_pending = false,
    retry_count = GREATEST(retry_count - 1, 0),
    updated_at = now()
WHERE id = $1 AND retry_pending
RETURNING *;

-- name: GetTaskBySessionID :one
SELECT * FROM task WHERE session_id = $1;

-- name: UpdateTaskTitleIfAuto :one
UPDATE task
SET title = $2, updated_at = now()
WHERE session_id = $1
  AND title_auto
  AND title IS DISTINCT FROM $2
RETURNING *;

-- name: UpdateTaskContent :one
UPDATE task
SET title = $2,
    prompt = $3,
    title_auto = $4,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateTaskSortOrder :exec
UPDATE task
SET sort_order = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateTaskArchivedAt :one
UPDATE task
SET archived_at = sqlc.narg('archived_at'),
    -- Archiving removes a queued task from its workspace's queue.
    queued_at = CASE
        WHEN sqlc.narg('archived_at')::timestamptz IS NULL THEN queued_at
        ELSE NULL
    END,
    updated_at = now()
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
