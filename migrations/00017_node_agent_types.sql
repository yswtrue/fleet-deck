-- +goose Up
-- agent_types: the adapter keys a node reported as runnable, kept separate
-- from capabilities (free-form labels) so clients can offer exactly the agents
-- a node can run. Rows registered before this migration are empty until their
-- next handshake.
ALTER TABLE node ADD COLUMN agent_types TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE node DROP COLUMN agent_types;
