-- +goose Up
-- Free-form node labels were display-only (agent keys live in agent_types);
-- drop the unused column.
ALTER TABLE node DROP COLUMN capabilities;

-- +goose Down
ALTER TABLE node ADD COLUMN capabilities TEXT[] NOT NULL DEFAULT '{}';
