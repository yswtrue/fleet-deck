-- +goose Up
-- auto_allow_scope is the workspace-level range in which ACP permission
-- requests (session/request_permission) naming file locations are approved
-- without pausing the operator:
--   none      ask for every request (the pre-setting behavior)
--   workspace approve requests whose locations all resolve inside path
--   all       approve requests whose locations are any directory
-- Requests that report no locations are never auto-approved: there is
-- nothing to check the range against.
ALTER TABLE workspace
    ADD COLUMN auto_allow_scope TEXT NOT NULL DEFAULT 'none'
        CHECK (auto_allow_scope IN ('none', 'workspace', 'all'));

-- +goose Down
ALTER TABLE workspace DROP COLUMN auto_allow_scope;
