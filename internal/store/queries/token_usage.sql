-- name: InsertTokenUsage :one
INSERT INTO token_usage (
    session_id, task_id, model, prompt_tokens, completion_tokens, total_tokens,
    cached_read_tokens, cached_write_tokens
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListTokenUsageBySession :many
SELECT * FROM token_usage
WHERE session_id = $1
ORDER BY created_at;

-- name: AggregateTokenUsage :many
SELECT
    tu.model,
    COALESCE(SUM(tu.prompt_tokens), 0)::bigint     AS prompt_tokens,
    COALESCE(SUM(tu.completion_tokens), 0)::bigint AS completion_tokens,
    COALESCE(SUM(tu.total_tokens), 0)::bigint      AS total_tokens,
    COALESCE(SUM(tu.cached_read_tokens), 0)::bigint  AS cached_read_tokens,
    COALESCE(SUM(tu.cached_write_tokens), 0)::bigint AS cached_write_tokens,
    COALESCE(SUM(tu.prompt_tokens) FILTER (
        WHERE tu.cached_read_tokens IS NOT NULL OR tu.cached_write_tokens IS NOT NULL
    ), 0)::bigint AS cache_prompt_tokens,
    COUNT(*) FILTER (
        WHERE tu.cached_read_tokens IS NOT NULL OR tu.cached_write_tokens IS NOT NULL
    )::bigint AS cache_reports,
    COUNT(*)::bigint                               AS turns
FROM token_usage tu
JOIN session s   ON s.id = tu.session_id
JOIN task t      ON t.id = tu.task_id
JOIN workspace w ON w.id = t.workspace_id
WHERE (sqlc.narg('session_id')::uuid IS NULL OR tu.session_id = sqlc.narg('session_id'))
  AND (sqlc.narg('workspace_id')::uuid IS NULL OR t.workspace_id = sqlc.narg('workspace_id'))
  AND (sqlc.narg('node_id')::uuid IS NULL OR w.node_id = sqlc.narg('node_id'))
  AND (sqlc.narg('from')::timestamptz IS NULL OR tu.created_at >= sqlc.narg('from'))
  AND (sqlc.narg('to')::timestamptz IS NULL OR tu.created_at <= sqlc.narg('to'))
  AND (sqlc.narg('member_user_id')::uuid IS NULL OR EXISTS (
        SELECT 1 FROM node_members nm
        WHERE nm.user_id = sqlc.narg('member_user_id') AND nm.node_id = w.node_id))
GROUP BY tu.model
ORDER BY SUM(tu.total_tokens) DESC, tu.model ASC;
