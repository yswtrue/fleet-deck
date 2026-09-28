-- +goose Up
-- message: conversation history for "open the task to enter the session".
CREATE TABLE message (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID        NOT NULL REFERENCES session (id) ON DELETE CASCADE,
    role       TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_role_check CHECK (role IN ('user', 'assistant', 'system'))
);

CREATE INDEX message_session_id_created_at_idx ON message (session_id, created_at);

-- event: append-only session event stream, supports replay / gap filling.
CREATE TABLE event (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id   UUID        NOT NULL REFERENCES session (id) ON DELETE CASCADE,
    type         TEXT        NOT NULL,
    payload_json JSONB       NOT NULL DEFAULT '{}',
    seq          BIGINT      NOT NULL DEFAULT 0,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX event_session_id_seq_idx ON event (session_id, seq);

-- +goose Down
DROP TABLE event;
DROP TABLE message;
