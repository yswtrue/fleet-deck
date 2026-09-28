package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// CreateMessageParams are the fields of one chat-history line.
type CreateMessageParams struct {
	SessionID string
	Role      protocol.MessageRole
	Content   string
}

// CreateMessage appends a message to a session's history and returns the row.
func (q *Queries) CreateMessage(ctx context.Context, p CreateMessageParams) (protocol.Message, error) {
	row, err := q.q.CreateMessage(ctx, sqlcgen.CreateMessageParams{
		SessionID: p.SessionID,
		Role:      string(p.Role),
		Content:   p.Content,
	})
	if err != nil {
		return protocol.Message{}, fmt.Errorf("store: create message: %w", err)
	}
	return messageFromRow(row), nil
}

// ListMessagesBySession returns up to limit messages for a session, oldest
// first (ordered by created_at). A limit <= 0 returns no rows.
func (q *Queries) ListMessagesBySession(ctx context.Context, sessionID string, limit int) ([]protocol.Message, error) {
	rows, err := q.q.ListMessagesBySession(ctx, sqlcgen.ListMessagesBySessionParams{
		SessionID: sessionID,
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: list messages by session: %w", err)
	}
	return messagesFromRows(rows), nil
}
