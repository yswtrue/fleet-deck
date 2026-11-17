package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"fleetdeck/internal/protocol"
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

// LatestSessionCommands returns the slash-command list the agent type most
// recently advertised in a workspace. Commands are session-scoped metadata
// persisted as ordinary events; the task form reads the last known list as
// completion hints before the new session has advertised its own. ok is false
// when no session of that agent type has reported a list in the workspace.
func (q *Queries) LatestSessionCommands(ctx context.Context, workspaceID, agentType string) (commands []protocol.SessionCommand, ok bool, err error) {
	payload, err := q.q.LatestWorkspaceCommands(ctx, sqlcgen.LatestWorkspaceCommandsParams{
		WorkspaceID: workspaceID,
		AgentType:   agentType,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: latest session commands: %w", err)
	}
	var stored protocol.SessionCommands
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, false, fmt.Errorf("store: decode session commands: %w", err)
	}
	return stored.Commands, true, nil
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
