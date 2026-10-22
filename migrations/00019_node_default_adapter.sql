-- +goose Up
-- default_adapter: the agent a node uses when a task names none. The node
-- daemon already resolves it for dispatch; advertising it lets the task form
-- preselect the operator's configured default instead of the first agent in
-- the (sorted) agent_types list. Rows registered before this migration keep
-- an empty value until their next handshake.
ALTER TABLE node ADD COLUMN default_adapter TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE node DROP COLUMN default_adapter;
