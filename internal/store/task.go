package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"

	"github.com/jackc/pgx/v5/pgtype"
)

// CreateTaskParams are the fields required to create a task.
type CreateTaskParams struct {
	WorkspaceID string
	Title       string
	Prompt      string
	Status      protocol.TaskStatus
	AgentType   string
	SortOrder   int
}

// CreateTask inserts a task and returns the stored row.
func (q *Queries) CreateTask(ctx context.Context, p CreateTaskParams) (protocol.Task, error) {
	row, err := q.q.CreateTask(ctx, sqlcgen.CreateTaskParams{
		WorkspaceID: p.WorkspaceID,
		Title:       p.Title,
		Prompt:      p.Prompt,
		Status:      string(p.Status),
		AgentType:   p.AgentType,
		SortOrder:   int32(p.SortOrder),
	})
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: create task: %w", err)
	}
	return taskFromRow(row), nil
}

// GetTask returns a task by id.
func (q *Queries) GetTask(ctx context.Context, id string) (protocol.Task, error) {
	row, err := q.q.GetTask(ctx, id)
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: get task: %w", err)
	}
	return taskFromRow(row), nil
}

// ListTasksByWorkspace returns a board's tasks ordered by sort_order.
func (q *Queries) ListTasksByWorkspace(ctx context.Context, workspaceID string) ([]protocol.Task, error) {
	rows, err := q.q.ListTasksByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks by workspace: %w", err)
	}
	return tasksFromRows(rows), nil
}

// ListTasksByNode returns every task on every workspace of a node, ordered by
// sort_order.
func (q *Queries) ListTasksByNode(ctx context.Context, nodeID string) ([]protocol.Task, error) {
	rows, err := q.q.ListTasksByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks by node: %w", err)
	}
	return tasksFromRows(rows), nil
}

// SearchTasksByWorkspace fuzzy-matches task titles on a board (pg_trgm:
// substring or trigram similarity), best match first.
func (q *Queries) SearchTasksByWorkspace(ctx context.Context, workspaceID, query string) ([]protocol.Task, error) {
	rows, err := q.q.SearchTasksByWorkspace(ctx, sqlcgen.SearchTasksByWorkspaceParams{
		WorkspaceID: workspaceID,
		Q:           queryText(query),
	})
	if err != nil {
		return nil, fmt.Errorf("store: search tasks by workspace: %w", err)
	}
	return tasksFromRows(rows), nil
}

// SearchTasksByNode fuzzy-matches task titles across every workspace of a
// node, best match first.
func (q *Queries) SearchTasksByNode(ctx context.Context, nodeID, query string) ([]protocol.Task, error) {
	rows, err := q.q.SearchTasksByNode(ctx, sqlcgen.SearchTasksByNodeParams{
		NodeID: nodeID,
		Q:      queryText(query),
	})
	if err != nil {
		return nil, fmt.Errorf("store: search tasks by node: %w", err)
	}
	return tasksFromRows(rows), nil
}

// queryText wraps a search term for the generated text parameters. Search
// terms are never NULL: an empty term means "no filter" and is handled by the
// callers falling back to ListTasksBy*.
func queryText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// UpdateTaskStatus transitions a task and returns the updated row.
func (q *Queries) UpdateTaskStatus(ctx context.Context, id string, status protocol.TaskStatus) (protocol.Task, error) {
	row, err := q.q.UpdateTaskStatus(ctx, sqlcgen.UpdateTaskStatusParams{
		ID:     id,
		Status: string(status),
	})
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: update task status: %w", err)
	}
	return taskFromRow(row), nil
}

// UpdateTaskSessionID points a task at its active session. An empty sessionID
// clears the forward pointer (stored as NULL).
func (q *Queries) UpdateTaskSessionID(ctx context.Context, id, sessionID string) error {
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	err := q.q.UpdateTaskSessionID(ctx, sqlcgen.UpdateTaskSessionIDParams{
		ID:        id,
		SessionID: sid,
	})
	if err != nil {
		return fmt.Errorf("store: update task session id: %w", err)
	}
	return nil
}

// GetTaskBySessionID resolves the task owning a session. Hot path: backed by the
// partial unique index task_session_id_idx.
func (q *Queries) GetTaskBySessionID(ctx context.Context, sessionID string) (protocol.Task, error) {
	row, err := q.q.GetTaskBySessionID(ctx, &sessionID)
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: get task by session id: %w", err)
	}
	return taskFromRow(row), nil
}

// UpdateTaskSortOrder repositions a task within its board.
func (q *Queries) UpdateTaskSortOrder(ctx context.Context, id string, sortOrder int) error {
	err := q.q.UpdateTaskSortOrder(ctx, sqlcgen.UpdateTaskSortOrderParams{
		ID:        id,
		SortOrder: int32(sortOrder),
	})
	if err != nil {
		return fmt.Errorf("store: update task sort order: %w", err)
	}
	return nil
}
