-- name: CreateNode :one
INSERT INTO node (name, host, status, agent_types, default_adapter, version, token_hash, machine_id, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg('machine_id'), now())
RETURNING *;

-- name: GetNode :one
SELECT * FROM node WHERE id = $1;

-- name: GetNodeByName :one
SELECT * FROM node WHERE name = $1;

-- name: GetNodeByMachineID :one
SELECT * FROM node WHERE machine_id = $1;

-- name: GetNodeMachineID :one
SELECT machine_id FROM node WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM node ORDER BY name;

-- name: UpdateNodeStatus :exec
UPDATE node
SET status = $2, last_seen_at = $3
WHERE id = $1;

-- name: TouchNodeLastSeen :exec
UPDATE node
SET last_seen_at = $2
WHERE id = $1;

-- name: MarkNodeOffline :exec
UPDATE node
SET status = 'offline'
WHERE id = $1;

-- name: MarkNodesOffline :exec
UPDATE node
SET status = 'offline'
WHERE status <> 'offline';

-- name: UpdateNodeTokenHash :exec
UPDATE node
SET token_hash = $2
WHERE id = $1;

-- name: UpdateNodeMachineID :exec
UPDATE node
SET machine_id = $2
WHERE id = $1;

-- name: UpdateNodeRuntime :exec
UPDATE node
SET agent_types = $2, default_adapter = $3, version = $4
WHERE id = $1;

-- NOTE: This query is not the auth path; bcrypt salting prevents hash equality
-- lookup. Used by tests and future token revocation.
-- name: GetNodeByTokenHash :one
SELECT * FROM node WHERE token_hash = $1;

-- Join tokens: single-use pairing credentials for a node's first registration.
-- The row lock in GetOpenNodeJoinToken serializes concurrent claims, so exactly
-- one registration can consume a token.

-- name: CreateNodeJoinToken :one
INSERT INTO node_join_token (token_hash, created_by, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOpenNodeJoinToken :one
SELECT * FROM node_join_token
WHERE token_hash = $1
  AND used_at IS NULL
  AND expires_at > now()
FOR UPDATE;

-- name: MarkNodeJoinTokenUsed :exec
UPDATE node_join_token
SET used_at = now(), used_node_id = $2
WHERE id = $1;
