-- +goose Up
-- version: the daemon release version reported in register_request
-- ("v1.2.3", or "dev" for source builds). Rows registered before this
-- migration keep an empty value until their next handshake.
ALTER TABLE node ADD COLUMN version TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE node DROP COLUMN version;
