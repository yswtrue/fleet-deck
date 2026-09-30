-- +goose Up
-- Privacy: session content is no longer persisted server-side. Chat history
-- lives in the agent's own session store on the node and is replayed on
-- demand (session history proxy); the hub keeps only task/session metadata
-- and status-class events.
DROP TABLE message;

-- +goose Down
CREATE TABLE message (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID        NOT NULL REFERENCES session (id) ON DELETE CASCADE,
    role       TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_role_check CHECK (role IN ('user', 'assistant', 'system'))
);

CREATE INDEX message_session_id_created_at_idx ON message (session_id, created_at);
