-- +goose Up
-- node: a daemon running on a target machine, connected to the hub over WS.
CREATE TABLE node (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT        NOT NULL,
    host         TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'offline',
    capabilities TEXT[]      NOT NULL DEFAULT '{}',
    token_hash   TEXT        NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT node_status_check CHECK (status IN ('online', 'offline'))
);

-- workspace: a board inside a node (1:1 board, no separate board table).
CREATE TABLE workspace (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID        NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    path       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX workspace_node_id_idx ON workspace (node_id);

-- +goose Down
DROP TABLE workspace;
DROP TABLE node;
