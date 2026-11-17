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

-- name: LatestWorkspaceCommands :one
-- The most recent command list advertised by one agent type in a workspace.
-- Command lists are session-scoped; the task form uses the last known list as
-- completion hints before the new session has advertised its own.
SELECT e.payload_json
FROM event e
JOIN session s ON s.id = e.session_id
JOIN task t ON t.id = s.task_id
WHERE t.workspace_id = $1
  AND s.agent_type = $2
  AND e.type = 'session.commands'
ORDER BY e.ts DESC
LIMIT 1;
