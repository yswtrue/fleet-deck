-- +goose Up
-- title_auto marks a provisional title: the caller omitted it on create, so
-- the hub derived one from the prompt and the agent may replace it with its
-- own session title (ACP session_info_update). A user-supplied title is never
-- overwritten.
ALTER TABLE task ADD COLUMN title_auto BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE task DROP COLUMN title_auto;
