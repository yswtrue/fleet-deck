-- +goose Up
-- subtask: the agent session's todo list, mirrored onto the board.
-- external_id is the agent-provided todo id used for diffing.
CREATE TABLE subtask (
    id          UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID    NOT NULL REFERENCES task (id) ON DELETE CASCADE,
    title       TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending',
    external_id TEXT    NOT NULL DEFAULT '',
    sort_order  INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT subtask_status_check CHECK (status IN ('pending', 'in_progress', 'done'))
);

CREATE INDEX subtask_task_id_sort_order_idx ON subtask (task_id, sort_order);

-- session: one run of an agent for a task on a node.
CREATE TABLE session (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id             UUID        NOT NULL REFERENCES task (id) ON DELETE CASCADE,
    node_id             UUID        NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    agent_type          TEXT        NOT NULL DEFAULT '',
    external_session_id TEXT        NOT NULL DEFAULT '',
    status              TEXT        NOT NULL DEFAULT 'waiting',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at            TIMESTAMPTZ NULL,
    CONSTRAINT session_status_check CHECK (status IN ('waiting', 'running', 'done', 'failed', 'awaiting_user'))
);

CREATE INDEX session_task_id_idx ON session (task_id);

-- +goose Down
DROP TABLE session;
DROP TABLE subtask;
