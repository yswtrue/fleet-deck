-- +goose Up
-- Task title fuzzy search (pg_trgm): trigram index accelerates both ILIKE
-- substring and similarity matching in SearchTasksBy*.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX task_title_trgm_idx ON task USING gin (title gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS task_title_trgm_idx;
DROP EXTENSION IF EXISTS pg_trgm;
