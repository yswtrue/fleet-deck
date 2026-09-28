-- name: AppendEvent :one
INSERT INTO event (session_id, type, payload_json, seq)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListEventsBySession :many
SELECT * FROM event
WHERE session_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;

-- name: MaxEventSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint AS max_seq
FROM event
WHERE session_id = $1;
