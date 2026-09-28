-- +goose Up
-- Two nodes may not share a name; prevents the registration handshake from
-- returning an arbitrary row when a duplicate slips through.
CREATE UNIQUE INDEX node_name_key ON node (name);

-- +goose Down
DROP INDEX IF EXISTS node_name_key;
