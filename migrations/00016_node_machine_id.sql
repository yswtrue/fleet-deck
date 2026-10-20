-- +goose Up
-- machine_id: stable, hashed identity of the physical/virtual machine a node
-- runs on. At most one node row may claim it, so two daemons on the same
-- computer cannot register under different names. NULL for rows registered
-- before this migration; they adopt an id on their next handshake.
ALTER TABLE node ADD COLUMN machine_id TEXT NULL;

CREATE UNIQUE INDEX node_machine_id_key ON node (machine_id) WHERE machine_id IS NOT NULL;

-- +goose Down
DROP INDEX node_machine_id_key;
ALTER TABLE node DROP COLUMN machine_id;
