-- +goose Up
-- task lifecycle: backlog tasks (auto_start=false are created waiting and only
-- dispatch on explicit start) and archiving (finished tasks hidden from the
-- default board view until restored).
ALTER TABLE task ADD COLUMN archived_at TIMESTAMPTZ NULL;
ALTER TABLE task ADD COLUMN auto_start BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE task DROP COLUMN auto_start;
ALTER TABLE task DROP COLUMN archived_at;
