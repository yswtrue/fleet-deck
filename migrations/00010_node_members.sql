-- +goose Up
-- node_members: which users are bound to which node. A node binding grants
-- read+write to the node's whole subtree (workspaces, tasks, sessions,
-- messages, events, subtasks); admins bypass binding. Binding is a
-- management-plane action (PUT /api/users/{id}/nodes), decoupled from
-- /ws/node registration.
CREATE TABLE node_members (
    user_id  UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    node_id  UUID        NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT node_members_pkey PRIMARY KEY (user_id, node_id)
);

CREATE INDEX node_members_user_id_idx ON node_members (user_id);
CREATE INDEX node_members_node_id_idx ON node_members (node_id);

-- workspace_members is superseded by node binding with subtree inheritance.
DROP TABLE workspace_members;

-- +goose Down
CREATE TABLE workspace_members (
    workspace_id UUID        NOT NULL REFERENCES workspace (id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workspace_members_pkey PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

DROP TABLE node_members;
