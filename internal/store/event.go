package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/store/sqlcgen"
)

// AppendEvent appends one event to a session's stream at the given sequence
// number and returns the persisted row.
func (q *Queries) AppendEvent(ctx context.Context, sessionID, eventType string, payload []byte, seq int64) (Event, error) {
	row, err := q.q.AppendEvent(ctx, sqlcgen.AppendEventParams{
		SessionID:   sessionID,
		Type:        eventType,
		PayloadJson: payload,
		Seq:         seq,
	})
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: %w", err)
	}
	return eventFromRow(row), nil
}

// ListEventsBySession returns up to limit events with seq strictly greater than
// afterSeq, oldest first. Pass afterSeq = 0 to start from the beginning; this is
// the reconnect/gap-fill path.
func (q *Queries) ListEventsBySession(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, error) {
	rows, err := q.q.ListEventsBySession(ctx, sqlcgen.ListEventsBySessionParams{
		SessionID: sessionID,
		Seq:       afterSeq,
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: list events by session: %w", err)
	}
	return eventsFromRows(rows), nil
}

// MaxEventSeq returns the highest sequence number in a session's stream, or 0
// when the session has no events yet.
func (q *Queries) MaxEventSeq(ctx context.Context, sessionID string) (int64, error) {
	seq, err := q.q.MaxEventSeq(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("store: max event seq: %w", err)
	}
	return seq, nil
}
