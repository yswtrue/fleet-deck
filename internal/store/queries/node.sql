-- name: CreateNode :one
INSERT INTO node (name, host, status, capabilities, token_hash, last_seen_at)
VALUES ($1, $2, $3, $4, $5, now())
RETURNING *;

-- name: GetNode :one
SELECT * FROM node WHERE id = $1;

-- name: GetNodeByName :one
SELECT * FROM node WHERE name = $1;

-- name: ListNodes :many
SELECT * FROM node ORDER BY name;

-- name: UpdateNodeStatus :exec
UPDATE node
SET status = $2, last_seen_at = $3
WHERE id = $1;

-- name: UpdateNodeTokenHash :exec
UPDATE node
SET token_hash = $2
WHERE id = $1;

-- NOTE: This query is not the auth path; bcrypt salting prevents hash equality
-- lookup. Used by tests and future token revocation.
-- name: GetNodeByTokenHash :one
SELECT * FROM node WHERE token_hash = $1;
