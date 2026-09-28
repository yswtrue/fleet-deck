-- name: CreateSession :one
INSERT INTO session (task_id, node_id, agent_type, external_session_id, status)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetSession :one
SELECT * FROM session WHERE id = $1;

-- name: UpdateSessionStatus :exec
UPDATE session
SET status = $2, ended_at = sqlc.narg('ended_at')
WHERE id = $1;

-- name: UpdateSessionExternalID :exec
UPDATE session
SET external_session_id = $2
WHERE id = $1;

-- name: ListSessionsByNode :many
SELECT * FROM session WHERE node_id = $1 ORDER BY created_at;

-- name: ListSessionsByTask :many
SELECT * FROM session WHERE task_id = $1 ORDER BY created_at;
