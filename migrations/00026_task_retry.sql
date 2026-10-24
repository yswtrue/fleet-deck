-- +goose Up
-- A node process restart kills the in-process adapters. retry_pending marks a
-- task whose running session was lost that way (or by the offline cascade), so
-- the hub re-dispatches it when its node reconnects; retry_count bounds the
-- automatic attempts (FLEETDECK_TASK_RETRY_LIMIT, 0 disables).
ALTER TABLE task ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task ADD COLUMN retry_pending BOOLEAN NOT NULL DEFAULT false;

-- The reconnect path scans pending rows per node; pending rows are rare, so a
-- partial index keeps the scan small without indexing the common false state.
CREATE INDEX task_retry_pending_idx ON task (workspace_id) WHERE retry_pending;

-- +goose Down
DROP INDEX task_retry_pending_idx;
ALTER TABLE task DROP COLUMN retry_pending;
ALTER TABLE task DROP COLUMN retry_count;
