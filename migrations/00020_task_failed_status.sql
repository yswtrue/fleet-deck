-- +goose Up
-- A failed session used to be recorded as a done task, which hid crashes and
-- node-offline cascades among the finished work. Failed is its own terminal
-- state so the board can surface it next to awaiting_user.
ALTER TABLE task DROP CONSTRAINT task_status_check;
ALTER TABLE task ADD CONSTRAINT task_status_check
    CHECK (status IN ('waiting', 'running', 'done', 'awaiting_user', 'failed'));

-- +goose Down
UPDATE task SET status = 'done' WHERE status = 'failed';
ALTER TABLE task DROP CONSTRAINT task_status_check;
ALTER TABLE task ADD CONSTRAINT task_status_check
    CHECK (status IN ('waiting', 'running', 'done', 'awaiting_user'));
