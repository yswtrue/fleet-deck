package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// CreateWorkspaceParams are the fields required to register a workspace.
type CreateWorkspaceParams struct {
	NodeID string
	Name   string
	Path   string
}

// CreateWorkspace inserts a workspace and returns the stored row.
func (q *Queries) CreateWorkspace(ctx context.Context, p CreateWorkspaceParams) (protocol.Workspace, error) {
	row, err := q.q.CreateWorkspace(ctx, sqlcgen.CreateWorkspaceParams{
		NodeID: p.NodeID,
		Name:   p.Name,
		Path:   p.Path,
	})
	if err != nil {
		return protocol.Workspace{}, fmt.Errorf("store: create workspace: %w", err)
	}
	return workspaceFromRow(row), nil
}

// GetWorkspace returns a workspace by id.
func (q *Queries) GetWorkspace(ctx context.Context, id string) (protocol.Workspace, error) {
	row, err := q.q.GetWorkspace(ctx, id)
	if err != nil {
		return protocol.Workspace{}, fmt.Errorf("store: get workspace: %w", err)
	}
	return workspaceFromRow(row), nil
}

// UpdateWorkspaceName renames a workspace and returns the updated row. The
// caller validates and trims the name.
func (q *Queries) UpdateWorkspaceName(ctx context.Context, id, name string) (protocol.Workspace, error) {
	row, err := q.q.UpdateWorkspaceName(ctx, sqlcgen.UpdateWorkspaceNameParams{
		ID:   id,
		Name: name,
	})
	if err != nil {
		return protocol.Workspace{}, fmt.Errorf("store: update workspace name: %w", err)
	}
	return workspaceFromRow(row), nil
}

// DeleteWorkspace removes a workspace and, by foreign-key cascade, its tasks,
// sessions, subtasks, events and token usage. Files on the node are untouched.
func (q *Queries) DeleteWorkspace(ctx context.Context, id string) error {
	if err := q.q.DeleteWorkspace(ctx, id); err != nil {
		return fmt.Errorf("store: delete workspace: %w", err)
	}
	return nil
}

// CountActiveSessionsByWorkspace counts non-terminal sessions under the
// workspace. Deleting a workspace with live adapters would orphan them on the
// node, so the hub refuses until they finish.
func (q *Queries) CountActiveSessionsByWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	n, err := q.q.CountActiveSessionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("store: count active sessions by workspace: %w", err)
	}
	return n, nil
}

// UpdateWorkspaceAutoAllowScope stores a workspace's permission auto-allow
// range and returns the updated row. The caller validates the enum.
func (q *Queries) UpdateWorkspaceAutoAllowScope(ctx context.Context, id string, scope protocol.AutoAllowScope) (protocol.Workspace, error) {
	row, err := q.q.UpdateWorkspaceAutoAllowScope(ctx, sqlcgen.UpdateWorkspaceAutoAllowScopeParams{
		ID:             id,
		AutoAllowScope: string(scope),
	})
	if err != nil {
		return protocol.Workspace{}, fmt.Errorf("store: update workspace auto_allow_scope: %w", err)
	}
	return workspaceFromRow(row), nil
}

// ListWorkspacesByNode returns a node's workspaces ordered by creation time.
func (q *Queries) ListWorkspacesByNode(ctx context.Context, nodeID string) ([]protocol.Workspace, error) {
	rows, err := q.q.ListWorkspacesByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list workspaces by node: %w", err)
	}
	return workspacesFromRows(rows), nil
}
