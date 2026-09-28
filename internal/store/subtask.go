package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// UpsertSubtaskByExternalIDParams are the fields of a mirrored todo item.
// ExternalID is the agent-provided todo id; it must be non-empty for the
// (task_id, external_id) unique index (migration 00006) to apply.
type UpsertSubtaskByExternalIDParams struct {
	TaskID     string
	Title      string
	Status     protocol.SubtaskStatus
	ExternalID string
	SortOrder  int
}

// UpsertSubtaskByExternalID creates or updates the subtask identified by
// (task_id, external_id), returning the stored row.
func (q *Queries) UpsertSubtaskByExternalID(ctx context.Context, p UpsertSubtaskByExternalIDParams) (protocol.Subtask, error) {
	row, err := q.q.UpsertSubtaskByExternalID(ctx, sqlcgen.UpsertSubtaskByExternalIDParams{
		TaskID:     p.TaskID,
		Title:      p.Title,
		Status:     string(p.Status),
		ExternalID: p.ExternalID,
		SortOrder:  int32(p.SortOrder),
	})
	if err != nil {
		return protocol.Subtask{}, fmt.Errorf("store: upsert subtask: %w", err)
	}
	return subtaskFromRow(row), nil
}

// DeleteSubtaskByExternalID removes the subtask with the given agent todo id.
func (q *Queries) DeleteSubtaskByExternalID(ctx context.Context, taskID, externalID string) error {
	err := q.q.DeleteSubtaskByExternalID(ctx, sqlcgen.DeleteSubtaskByExternalIDParams{
		TaskID:     taskID,
		ExternalID: externalID,
	})
	if err != nil {
		return fmt.Errorf("store: delete subtask: %w", err)
	}
	return nil
}

// ListSubtasksByTask returns a task's subtasks ordered by sort_order.
func (q *Queries) ListSubtasksByTask(ctx context.Context, taskID string) ([]protocol.Subtask, error) {
	rows, err := q.q.ListSubtasksByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list subtasks by task: %w", err)
	}
	return subtasksFromRows(rows), nil
}
