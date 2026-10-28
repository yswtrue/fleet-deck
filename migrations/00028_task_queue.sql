-- +goose Up
-- Workspace task queue: a workspace runs one task at a time, so a task that
-- cannot start (the workspace is busy or its node is offline) waits with
-- queued_at set and starts in that order once a slot frees. NULL means the
-- task is not queued.
ALTER TABLE task ADD COLUMN queued_at TIMESTAMPTZ NULL;

-- The drain path looks up a workspace's oldest queued task.
CREATE INDEX task_queued_workspace_idx
    ON task (workspace_id, queued_at)
    WHERE queued_at IS NOT NULL;

-- +goose Down
DROP INDEX task_queued_workspace_idx;
ALTER TABLE task DROP COLUMN queued_at;
