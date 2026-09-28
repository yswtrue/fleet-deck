-- name: CreateMessage :one
INSERT INTO message (session_id, role, content)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListMessagesBySession :many
SELECT * FROM message
WHERE session_id = $1
ORDER BY created_at, id
LIMIT $2;
