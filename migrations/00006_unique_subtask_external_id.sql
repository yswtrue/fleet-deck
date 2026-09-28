-- +goose Up
-- Node mirrors an agent session's todo list onto the board by diffing on the
-- agent-provided todo id (subtask.external_id). Correct upsert-by-external-id
-- needs (task_id, external_id) to be unique within a task. external_id uses
-- the empty-string sentinel for "no agent id yet" (hand-created subtasks), so
-- the index is partial and only covers real external ids.
CREATE UNIQUE INDEX subtask_task_id_external_id_key
    ON subtask (task_id, external_id)
    WHERE external_id <> '';

-- +goose Down
DROP INDEX IF EXISTS subtask_task_id_external_id_key;
