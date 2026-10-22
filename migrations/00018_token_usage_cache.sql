-- +goose Up
-- Optional prompt-cache telemetry for one reported turn. NULL means the agent
-- reported no cache category (the model exposes none); an explicit zero is a
-- reported miss. ACP reports prompt_tokens as the non-cached input portion, so
-- the full input is prompt_tokens + cached_read_tokens + cached_write_tokens.
ALTER TABLE token_usage
    ADD COLUMN cached_read_tokens  BIGINT,
    ADD COLUMN cached_write_tokens BIGINT;

-- +goose Down
ALTER TABLE token_usage
    DROP COLUMN cached_write_tokens,
    DROP COLUMN cached_read_tokens;
