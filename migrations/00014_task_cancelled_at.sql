-- +goose Up
-- Manual termination is distinguishable from agent completion without adding
-- a fifth task status. Historical cancellations cannot be inferred reliably.
ALTER TABLE task ADD COLUMN cancelled_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE task DROP COLUMN cancelled_at;
