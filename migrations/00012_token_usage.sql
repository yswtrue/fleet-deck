-- +goose Up
-- token_usage: one row per turn's reported model token usage for a session.
-- Numbers come verbatim from what the agent reported (adapter -> node -> hub);
-- a session whose agent reports no usage produces no rows.
CREATE TABLE token_usage (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id        UUID        NOT NULL REFERENCES session (id) ON DELETE CASCADE,
    task_id           UUID        NOT NULL REFERENCES task (id) ON DELETE CASCADE,
    model             TEXT        NOT NULL,
    prompt_tokens     BIGINT      NOT NULL,
    completion_tokens BIGINT      NOT NULL,
    total_tokens      BIGINT      NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX token_usage_task_id_idx ON token_usage (task_id);
CREATE INDEX token_usage_session_id_idx ON token_usage (session_id);
CREATE INDEX token_usage_created_at_idx ON token_usage (created_at);

-- +goose Down
DROP TABLE token_usage;
