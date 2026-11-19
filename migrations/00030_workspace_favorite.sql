-- +goose Up
-- workspace_favorite: per-user workspace bookmarks. A favorite is visible to
-- its owner only and never grants access: list reads still require the
-- workspace's node to be visible (node_members / admin).
CREATE TABLE workspace_favorite (
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id UUID        NOT NULL REFERENCES workspace (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workspace_favorite_pkey PRIMARY KEY (user_id, workspace_id)
);

CREATE INDEX workspace_favorite_user_created_idx
    ON workspace_favorite (user_id, created_at DESC);

-- +goose Down
DROP TABLE workspace_favorite;
