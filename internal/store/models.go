package store

import (
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// This file defines the store-internal DB-row models and the conversion
// boundary to the wire types. The convention:
//
//   - The sqlc-generated types in internal/store/sqlcgen are the DB-row types.
//     Postgres NULLs are mapped to Go pointers by sqlc overrides (*string for a
//     nullable uuid such as task.session_id, *time.Time for a nullable
//     timestamptz such as session.ended_at). Empty-string sentinels
//     (external_id, external_session_id, token_hash) stay plain string.
//   - The public API of this package speaks protocol.* types. Conversion
//     happens here, so hub/node never see sqlc or pgtype details.
//   - Wire-invisible columns are dropped at this boundary (e.g.
//     node.token_hash is never surfaced through protocol.Node).
//   - Nullable DB columns map to the omitempty / pointer wire fields: a nil
//     pointer becomes the protocol zero value ("" or a nil *time.Time).

// Event is one persisted row of the append-only session event stream. There is
// no protocol.* equivalent: protocol.Event is the hub->web WS push envelope,
// not a database row.
type Event struct {
	ID        string
	SessionID string
	Type      string
	Payload   []byte
	Seq       int64
	Ts        time.Time
}

func eventFromRow(r sqlcgen.Event) Event {
	return Event{
		ID:        r.ID,
		SessionID: r.SessionID,
		Type:      r.Type,
		Payload:   r.PayloadJson,
		Seq:       r.Seq,
		Ts:        r.Ts,
	}
}

func eventsFromRows(rows []sqlcgen.Event) []Event {
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out
}

func nodeFromRow(r sqlcgen.Node) protocol.Node {
	caps := r.Capabilities
	if caps == nil {
		caps = []string{}
	}
	return protocol.Node{
		ID:           r.ID,
		Name:         r.Name,
		Host:         r.Host,
		Status:       protocol.NodeStatus(r.Status),
		Capabilities: caps,
		LastSeenAt:   r.LastSeenAt,
	}
}

func nodesFromRows(rows []sqlcgen.Node) []protocol.Node {
	out := make([]protocol.Node, 0, len(rows))
	for _, r := range rows {
		out = append(out, nodeFromRow(r))
	}
	return out
}

func workspaceFromRow(r sqlcgen.Workspace) protocol.Workspace {
	return protocol.Workspace{
		ID:        r.ID,
		NodeID:    r.NodeID,
		Name:      r.Name,
		Path:      r.Path,
		CreatedAt: r.CreatedAt,
	}
}

func workspacesFromRows(rows []sqlcgen.Workspace) []protocol.Workspace {
	out := make([]protocol.Workspace, 0, len(rows))
	for _, r := range rows {
		out = append(out, workspaceFromRow(r))
	}
	return out
}

func taskFromRow(r sqlcgen.Task) protocol.Task {
	var sessionID string
	if r.SessionID != nil {
		sessionID = *r.SessionID
	}
	return protocol.Task{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		Title:       r.Title,
		Prompt:      r.Prompt,
		Status:      protocol.TaskStatus(r.Status),
		AgentType:   r.AgentType,
		SessionID:   sessionID,
		SortOrder:   int(r.SortOrder),
		AutoStart:   r.AutoStart,
		ArchivedAt:  r.ArchivedAt,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func tasksFromRows(rows []sqlcgen.Task) []protocol.Task {
	out := make([]protocol.Task, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromRow(r))
	}
	return out
}

func subtaskFromRow(r sqlcgen.Subtask) protocol.Subtask {
	return protocol.Subtask{
		ID:         r.ID,
		TaskID:     r.TaskID,
		Title:      r.Title,
		Status:     protocol.SubtaskStatus(r.Status),
		ExternalID: r.ExternalID,
		SortOrder:  int(r.SortOrder),
	}
}

func subtasksFromRows(rows []sqlcgen.Subtask) []protocol.Subtask {
	out := make([]protocol.Subtask, 0, len(rows))
	for _, r := range rows {
		out = append(out, subtaskFromRow(r))
	}
	return out
}

func sessionFromRow(r sqlcgen.Session) protocol.Session {
	return protocol.Session{
		ID:                r.ID,
		TaskID:            r.TaskID,
		NodeID:            r.NodeID,
		AgentType:         r.AgentType,
		ExternalSessionID: r.ExternalSessionID,
		Status:            protocol.SessionStatus(r.Status),
		CreatedAt:         r.CreatedAt,
		EndedAt:           r.EndedAt,
	}
}

func sessionsFromRows(rows []sqlcgen.Session) []protocol.Session {
	out := make([]protocol.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionFromRow(r))
	}
	return out
}

func messageFromRow(r sqlcgen.Message) protocol.Message {
	return protocol.Message{
		ID:        r.ID,
		SessionID: r.SessionID,
		Role:      protocol.MessageRole(r.Role),
		Content:   r.Content,
		CreatedAt: r.CreatedAt,
	}
}

func messagesFromRows(rows []sqlcgen.Message) []protocol.Message {
	out := make([]protocol.Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, messageFromRow(r))
	}
	return out
}
