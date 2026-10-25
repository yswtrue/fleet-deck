-- +goose Up
-- error: why the session last failed (adapter exit, lost node process, ...).
-- Diagnostic metadata, not conversation content: it is shown next to the
-- failed status and cleared when the session runs again.
ALTER TABLE session ADD COLUMN error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session DROP COLUMN error;
