-- +goose Up
-- task: one agent session on a board. session_id is a forward pointer to the
-- active session; intentionally has no FK to avoid a cycle with session.
CREATE TABLE task (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES workspace (id) ON DELETE CASCADE,
    title        TEXT        NOT NULL,
    prompt       TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'waiting',
    agent_type   TEXT        NOT NULL DEFAULT '',
    session_id   UUID        NULL,
    sort_order   INTEGER     NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT task_status_check CHECK (status IN ('waiting', 'running', 'done', 'awaiting_user'))
);

CREATE INDEX task_workspace_id_sort_order_idx ON task (workspace_id, sort_order);

-- +goose Down
DROP TABLE task;
