-- +goose Up
-- Optional reasoning/thought tokens for one reported turn. NULL means the
-- agent reported no thought count; it is a separate subtype from the output
-- tokens and is also included in the agent-reported total_tokens.
ALTER TABLE token_usage
    ADD COLUMN thought_tokens BIGINT;

-- +goose Down
ALTER TABLE token_usage
    DROP COLUMN thought_tokens;
