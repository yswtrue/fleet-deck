package store

import (
	"context"
	"fmt"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// CreateSessionParams are the fields required to open an agent session.
type CreateSessionParams struct {
	TaskID            string
	NodeID            string
	AgentType         string
	ExternalSessionID string
	Status            protocol.SessionStatus
}

// CreateSession inserts a session and returns the stored row.
func (q *Queries) CreateSession(ctx context.Context, p CreateSessionParams) (protocol.Session, error) {
	row, err := q.q.CreateSession(ctx, sqlcgen.CreateSessionParams{
		TaskID:            p.TaskID,
		NodeID:            p.NodeID,
		AgentType:         p.AgentType,
		ExternalSessionID: p.ExternalSessionID,
		Status:            string(p.Status),
	})
	if err != nil {
		return protocol.Session{}, fmt.Errorf("store: create session: %w", err)
	}
	return sessionFromRow(row), nil
}

// GetSession returns a session by id.
func (q *Queries) GetSession(ctx context.Context, id string) (protocol.Session, error) {
	row, err := q.q.GetSession(ctx, id)
	if err != nil {
		return protocol.Session{}, fmt.Errorf("store: get session: %w", err)
	}
	return sessionFromRow(row), nil
}

// UpdateSessionStatus transitions a session. Pass a non-nil endedAt when the
// session reaches a terminal state; pass nil to clear it.
func (q *Queries) UpdateSessionStatus(ctx context.Context, id string, status protocol.SessionStatus, endedAt *time.Time) error {
	err := q.q.UpdateSessionStatus(ctx, sqlcgen.UpdateSessionStatusParams{
		ID:      id,
		Status:  string(status),
		EndedAt: endedAt,
	})
	if err != nil {
		return fmt.Errorf("store: update session status: %w", err)
	}
	return nil
}

// UpdateSessionExternalID records the agent-side session identifier.
func (q *Queries) UpdateSessionExternalID(ctx context.Context, id, ext string) error {
	err := q.q.UpdateSessionExternalID(ctx, sqlcgen.UpdateSessionExternalIDParams{
		ID:                id,
		ExternalSessionID: ext,
	})
	if err != nil {
		return fmt.Errorf("store: update session external id: %w", err)
	}
	return nil
}

// ListSessionsByNode returns a node's sessions ordered by creation time. Hot
// path when a node goes offline; backed by session_node_id_idx.
func (q *Queries) ListSessionsByNode(ctx context.Context, nodeID string) ([]protocol.Session, error) {
	rows, err := q.q.ListSessionsByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions by node: %w", err)
	}
	return sessionsFromRows(rows), nil
}

// ListSessionsByTask returns a task's sessions ordered by creation time.
func (q *Queries) ListSessionsByTask(ctx context.Context, taskID string) ([]protocol.Session, error) {
	rows, err := q.q.ListSessionsByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions by task: %w", err)
	}
	return sessionsFromRows(rows), nil
}
