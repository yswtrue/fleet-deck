-- +goose Up
-- users: hub operators. Email is stored lowercase+trimmed (app-enforced) and
-- unique; role is one of 'admin' / 'user'. The first user is created through
-- POST /api/auth/setup and is always an admin.
CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        NOT NULL,
    display_name  TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_role_check CHECK (role IN ('admin', 'user'))
);

-- auth_tokens: opaque bearer tokens for HTTP/WebSocket access. The stored
-- token_hash is the sha256 hex of the token (deterministic, queryable);
-- the plaintext token is returned to the caller exactly once at creation.
CREATE TABLE auth_tokens (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL,
    CONSTRAINT auth_tokens_hash_unique UNIQUE (token_hash)
);

CREATE INDEX auth_tokens_token_hash_idx ON auth_tokens (token_hash);
CREATE INDEX auth_tokens_user_id_idx ON auth_tokens (user_id);

-- workspace_members: which users may see and act on a workspace. Admins
-- bypass membership; non-admin access is granted row by row.
CREATE TABLE workspace_members (
    workspace_id UUID        NOT NULL REFERENCES workspace (id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workspace_members_pkey PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

-- +goose Down
DROP TABLE workspace_members;
DROP TABLE auth_tokens;
DROP TABLE users;
