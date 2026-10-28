-- +goose Up
-- The workspace task queue is gone: a task starts when it is dispatched, so
-- queued_at is no longer read or written. Drop the marker and its index.
DROP INDEX IF EXISTS task_queued_workspace_idx;
ALTER TABLE task DROP COLUMN IF EXISTS queued_at;

-- +goose Down
ALTER TABLE task ADD COLUMN queued_at TIMESTAMPTZ NULL;
CREATE INDEX task_queued_workspace_idx
    ON task (workspace_id, queued_at)
    WHERE queued_at IS NOT NULL;
