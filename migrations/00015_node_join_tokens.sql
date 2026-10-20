-- +goose Up
-- node_join_token: single-use pairing tokens minted by an authenticated user.
-- A node that is not registered yet must present one during the /ws/node
-- handshake; the hub then creates the node and binds the token's creator to it,
-- so a machine cannot be claimed by whoever reaches the hub first. The
-- plaintext token is never stored (token_hash is the sha256 hex, like
-- auth_tokens).
CREATE TABLE node_join_token (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   TEXT        NOT NULL,
    created_by   UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ NULL,
    used_node_id UUID        NULL REFERENCES node (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT node_join_token_hash_unique UNIQUE (token_hash)
);

CREATE INDEX node_join_token_created_by_idx ON node_join_token (created_by);

-- +goose Down
DROP TABLE node_join_token;
