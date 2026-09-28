-- name: UpsertSubtaskByExternalID :one
INSERT INTO subtask (task_id, title, status, external_id, sort_order)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (task_id, external_id) WHERE external_id <> ''
DO UPDATE SET
    title = EXCLUDED.title,
    status = EXCLUDED.status,
    sort_order = EXCLUDED.sort_order
RETURNING *;

-- name: DeleteSubtaskByExternalID :exec
DELETE FROM subtask WHERE task_id = $1 AND external_id = $2;

-- name: ListSubtasksByTask :many
SELECT * FROM subtask WHERE task_id = $1 ORDER BY sort_order, id;
