package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateTaskParams are the fields required to create a task. AutoStart
// defaults to true: pass an explicit false to create a backlog task that
// stays waiting until POST /api/tasks/{id}/start dispatches it.
type CreateTaskParams struct {
	WorkspaceID string
	Title       string
	Prompt      string
	Status      protocol.TaskStatus
	AgentType   string
	SortOrder   int
	AutoStart   bool
	// TitleAuto marks a prompt-derived title the agent may replace with its
	// own session title. False means the caller chose the title.
	TitleAuto bool
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
		AutoStart:   p.AutoStart,
		TitleAuto:   p.TitleAuto,
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

// ListTasksByWorkspace returns a board's unarchived tasks ordered by
// sort_order. Archived rows are hidden by default; use
// ListArchivedTasksByWorkspace with ?archived=1.
func (q *Queries) ListTasksByWorkspace(ctx context.Context, workspaceID string) ([]protocol.Task, error) {
	rows, err := q.q.ListTasksByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks by workspace: %w", err)
	}
	return tasksFromRows(rows), nil
}

// ListArchivedTasksByWorkspace returns only a board's archived tasks.
func (q *Queries) ListArchivedTasksByWorkspace(ctx context.Context, workspaceID string) ([]protocol.Task, error) {
	rows, err := q.q.ListArchivedTasksByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list archived tasks by workspace: %w", err)
	}
	return tasksFromRows(rows), nil
}

// ListTasksByNode returns every unarchived task on every workspace of a
// node, ordered by sort_order.
func (q *Queries) ListTasksByNode(ctx context.Context, nodeID string) ([]protocol.Task, error) {
	rows, err := q.q.ListTasksByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks by node: %w", err)
	}
	return tasksFromRows(rows), nil
}

// ListArchivedTasksByNode returns only a node's archived tasks.
func (q *Queries) ListArchivedTasksByNode(ctx context.Context, nodeID string) ([]protocol.Task, error) {
	rows, err := q.q.ListArchivedTasksByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list archived tasks by node: %w", err)
	}
	return tasksFromRows(rows), nil
}

// SearchTasksByWorkspace fuzzy-matches unarchived task titles on a board
// (pg_trgm: substring or trigram similarity), best match first.
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

// SearchArchivedTasksByWorkspace fuzzy-matches only archived task titles on a
// board.
func (q *Queries) SearchArchivedTasksByWorkspace(ctx context.Context, workspaceID, query string) ([]protocol.Task, error) {
	rows, err := q.q.SearchArchivedTasksByWorkspace(ctx, sqlcgen.SearchArchivedTasksByWorkspaceParams{
		WorkspaceID: workspaceID,
		Q:           queryText(query),
	})
	if err != nil {
		return nil, fmt.Errorf("store: search archived tasks by workspace: %w", err)
	}
	return tasksFromRows(rows), nil
}

// SearchTasksByNode fuzzy-matches unarchived task titles across every
// workspace of a node, best match first.
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

// SearchArchivedTasksByNode fuzzy-matches only archived task titles across a
// node.
func (q *Queries) SearchArchivedTasksByNode(ctx context.Context, nodeID, query string) ([]protocol.Task, error) {
	rows, err := q.q.SearchArchivedTasksByNode(ctx, sqlcgen.SearchArchivedTasksByNodeParams{
		NodeID: nodeID,
		Q:      queryText(query),
	})
	if err != nil {
		return nil, fmt.Errorf("store: search archived tasks by node: %w", err)
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

// UpdateTaskStatusManual records the first manual termination atomically with
// the status change. Agent-derived transitions use UpdateTaskStatus instead.
func (q *Queries) UpdateTaskStatusManual(ctx context.Context, id string, status protocol.TaskStatus) (protocol.Task, error) {
	row, err := q.q.UpdateTaskStatusManual(ctx, sqlcgen.UpdateTaskStatusManualParams{
		ID:     id,
		Status: string(status),
	})
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: update task status manually: %w", err)
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

// UpdateTaskTitleIfAuto replaces a provisional (prompt-derived) task title
// with the title the agent assigned to its session. It reports false when no
// row changed: the task has a user-chosen title, the agent repeated the current
// title, or the session has no task.
func (q *Queries) UpdateTaskTitleIfAuto(ctx context.Context, sessionID, title string) (protocol.Task, bool, error) {
	row, err := q.q.UpdateTaskTitleIfAuto(ctx, sqlcgen.UpdateTaskTitleIfAutoParams{
		SessionID: &sessionID,
		Title:     title,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return protocol.Task{}, false, nil
	}
	if err != nil {
		return protocol.Task{}, false, fmt.Errorf("store: update task title: %w", err)
	}
	return taskFromRow(row), true, nil
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

// UpdateTaskArchivedAt sets (archive) or clears (unarchive, nil) a task's
// archived_at marker and returns the updated row.
func (q *Queries) UpdateTaskArchivedAt(ctx context.Context, id string, archivedAt *time.Time) (protocol.Task, error) {
	row, err := q.q.UpdateTaskArchivedAt(ctx, sqlcgen.UpdateTaskArchivedAtParams{
		ID:         id,
		ArchivedAt: archivedAt,
	})
	if err != nil {
		return protocol.Task{}, fmt.Errorf("store: update task archived_at: %w", err)
	}
	return taskFromRow(row), nil
}

// TaskStatsFilter narrows AggregateTaskStats. Empty ids and nil times mean
// "no restriction"; MemberUserID, when set, keeps only tasks whose workspace
// lives on a node the user is bound to (non-admin visibility).
type TaskStatsFilter struct {
	WorkspaceID  string
	NodeID       string
	From         *time.Time
	To           *time.Time
	MemberUserID string
}

// AggregateTaskStats counts the tasks created in the window (split by their
// current status) and the tasks that reached `done` with a last update inside
// the window. Archived tasks count like any other row.
func (q *Queries) AggregateTaskStats(ctx context.Context, f TaskStatsFilter) (protocol.TaskStats, error) {
	totals := sqlcgen.AggregateTaskStatsParams{
		From: f.From,
		To:   f.To,
	}
	byStatusParams := sqlcgen.AggregateTaskStatsByStatusParams{
		From: f.From,
		To:   f.To,
	}
	if f.WorkspaceID != "" {
		totals.WorkspaceID = &f.WorkspaceID
		byStatusParams.WorkspaceID = &f.WorkspaceID
	}
	if f.NodeID != "" {
		totals.NodeID = &f.NodeID
		byStatusParams.NodeID = &f.NodeID
	}
	if f.MemberUserID != "" {
		totals.MemberUserID = &f.MemberUserID
		byStatusParams.MemberUserID = &f.MemberUserID
	}

	row, err := q.q.AggregateTaskStats(ctx, totals)
	if err != nil {
		return protocol.TaskStats{}, fmt.Errorf("store: aggregate task stats: %w", err)
	}
	rows, err := q.q.AggregateTaskStatsByStatus(ctx, byStatusParams)
	if err != nil {
		return protocol.TaskStats{}, fmt.Errorf("store: aggregate task stats by status: %w", err)
	}

	out := protocol.TaskStats{
		Created:   row.Created,
		Completed: row.Completed,
		ByStatus:  make([]protocol.TaskStatsStatusCount, 0, len(rows)),
	}
	for _, r := range rows {
		out.ByStatus = append(out.ByStatus, protocol.TaskStatsStatusCount{
			Status: protocol.TaskStatus(r.Status),
			Count:  r.TaskCount,
		})
	}
	return out, nil
}
