-- +goose Up
-- Hot lookup path: every node->hub event resolves session_id -> task.
-- Partial unique index enforces "at most one active session per task" (forward
-- pointer semantics) while staying valid for the waiting state where the
-- pointer is NULL.
CREATE UNIQUE INDEX task_session_id_idx
    ON task (session_id)
    WHERE session_id IS NOT NULL;

-- Hot lookup path: when a node goes offline, hub finds all active sessions
-- on that node to mark stale/failed.
CREATE INDEX session_node_id_idx ON session (node_id);

-- +goose Down
DROP INDEX IF EXISTS session_node_id_idx;
DROP INDEX IF EXISTS task_session_id_idx;
