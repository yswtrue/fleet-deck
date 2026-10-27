package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"fleetdeck/internal/protocol"
)

// ---- fleetdeck_session_status ----------------------------------------------

// SessionStatusInput is the argument set of fleetdeck_session_status.
type SessionStatusInput struct {
	SessionID string `json:"session_id" jsonschema:"FleetDeck session id"`
}

// SessionStatusOutput is the session plus its parent task (null when unresolved).
type SessionStatusOutput struct {
	Session SessionSummary `json:"session"`
	Task    *TaskSummary   `json:"task"`
}

// SessionSummary is the compact session view returned by session_status.
type SessionSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	TaskID string `json:"task_id"`
}

// TaskSummary is the compact task view returned by session_status.
type TaskSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func registerSessionStatus(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_session_status",
		Description: "Get a FleetDeck session's status together with its parent task's status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SessionStatusInput) (*mcp.CallToolResult, SessionStatusOutput, error) {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, SessionStatusOutput{}, errors.New("session_id is required")
		}
		snap, err := hub.GetSession(ctx, in.SessionID)
		if err != nil {
			return nil, SessionStatusOutput{}, err
		}
		out := SessionStatusOutput{
			Session: SessionSummary{
				ID:     snap.Session.ID,
				Status: string(snap.Session.Status),
				TaskID: snap.Session.TaskID,
			},
		}
		if snap.Task != nil {
			out.Task = &TaskSummary{ID: snap.Task.ID, Status: string(snap.Task.Status)}
		}
		return nil, out, nil
	})
}

// ---- fleetdeck_task_list ---------------------------------------------------

// TaskListInput is the argument set of fleetdeck_task_list. At least one of
// workspace_id or node_id must be supplied; status is an optional filter.
type TaskListInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"list only tasks on this workspace's board"`
	NodeID      string `json:"node_id,omitempty" jsonschema:"list tasks across every workspace on this node"`
	Status      string `json:"status,omitempty" jsonschema:"optional task status filter: waiting, running, done or awaiting_user"`
}

// TaskListOutput is the board's tasks.
type TaskListOutput struct {
	Tasks []TaskListItem `json:"tasks"`
}

// TaskListItem is one task row.
type TaskListItem struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	SessionID *string `json:"session_id,omitempty"`
}

func registerTaskList(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_task_list",
		Description: "List FleetDeck tasks on a workspace, or across all workspaces on a node.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TaskListInput) (*mcp.CallToolResult, TaskListOutput, error) {
		if in.WorkspaceID == "" && in.NodeID == "" {
			return nil, TaskListOutput{}, errors.New("workspace_id or node_id is required")
		}
		if in.Status != "" && !validTaskStatus(in.Status) {
			return nil, TaskListOutput{}, fmt.Errorf("invalid status %q: want waiting, running, done or awaiting_user", in.Status)
		}

		var tasks []protocol.Task
		if in.WorkspaceID != "" {
			found, err := hub.ListTasksByWorkspace(ctx, in.WorkspaceID)
			if err != nil {
				return nil, TaskListOutput{}, err
			}
			tasks = found
		} else {
			// One round trip: GET /api/tasks?node_id= spans every workspace
			// on the node (was a per-workspace N+1 loop).
			found, err := hub.ListTasksByNode(ctx, in.NodeID)
			if err != nil {
				return nil, TaskListOutput{}, err
			}
			tasks = found
		}

		out := TaskListOutput{Tasks: make([]TaskListItem, 0, len(tasks))}
		for _, t := range tasks {
			if in.Status != "" && string(t.Status) != in.Status {
				continue
			}
			item := TaskListItem{ID: t.ID, Title: t.Title, Status: string(t.Status)}
			if t.SessionID != "" {
				sessionID := t.SessionID
				item.SessionID = &sessionID
			}
			out.Tasks = append(out.Tasks, item)
		}
		return nil, out, nil
	})
}

func validTaskStatus(status string) bool {
	switch protocol.TaskStatus(status) {
	case protocol.TaskWaiting, protocol.TaskRunning, protocol.TaskDone, protocol.TaskAwaitingUser:
		return true
	default:
		return false
	}
}

// ---- fleetdeck_subtask_list ------------------------------------------------

// SubtaskListInput is the argument set of fleetdeck_subtask_list.
type SubtaskListInput struct {
	TaskID string `json:"task_id" jsonschema:"FleetDeck task id whose mirrored todos to list"`
}

// SubtaskListOutput is the task's mirrored todo items.
type SubtaskListOutput struct {
	Subtasks []SubtaskItem `json:"subtasks"`
}

// SubtaskItem is one mirrored todo item.
type SubtaskItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
}

func registerSubtaskList(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_subtask_list",
		Description: "List the subtasks mirrored from a FleetDeck task's agent todo list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SubtaskListInput) (*mcp.CallToolResult, SubtaskListOutput, error) {
		if strings.TrimSpace(in.TaskID) == "" {
			return nil, SubtaskListOutput{}, errors.New("task_id is required")
		}
		subtasks, err := hub.ListSubtasks(ctx, in.TaskID)
		if err != nil {
			return nil, SubtaskListOutput{}, err
		}
		out := SubtaskListOutput{Subtasks: make([]SubtaskItem, 0, len(subtasks))}
		for _, s := range subtasks {
			out.Subtasks = append(out.Subtasks, SubtaskItem{
				ID:         s.ID,
				Title:      s.Title,
				Status:     string(s.Status),
				ExternalID: s.ExternalID,
			})
		}
		return nil, out, nil
	})
}

// ---- fleetdeck_message_tail ------------------------------------------------

// MessageTailInput is the argument set of fleetdeck_message_tail.
type MessageTailInput struct {
	SessionID string `json:"session_id" jsonschema:"FleetDeck session id whose chat history to read"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of recent messages to return (default 20)"`
}

// MessageTailOutput is the tail of a session's chat history.
type MessageTailOutput struct {
	Messages []MessageItem `json:"messages"`
}

// MessageItem is one chat message.
type MessageItem struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func registerMessageTail(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_message_tail",
		Description: "Read the most recent messages of a FleetDeck session's chat history.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in MessageTailInput) (*mcp.CallToolResult, MessageTailOutput, error) {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, MessageTailOutput{}, errors.New("session_id is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		messages, err := hub.ListMessages(ctx, in.SessionID, limit)
		if err != nil {
			return nil, MessageTailOutput{}, err
		}
		out := MessageTailOutput{Messages: make([]MessageItem, 0, len(messages))}
		for _, m := range messages {
			out.Messages = append(out.Messages, MessageItem{
				ID:        m.ID,
				Role:      string(m.Role),
				Content:   m.Content,
				CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		return nil, out, nil
	})
}

// ---- fleetdeck_event_tail --------------------------------------------------

// EventTailInput is the argument set of fleetdeck_event_tail.
type EventTailInput struct {
	SessionID string `json:"session_id" jsonschema:"FleetDeck session id whose events to read"`
	AfterSeq  int64  `json:"after_seq,omitempty" jsonschema:"return only events with a per-session seq greater than this (default 0)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of events to return (default 100)"`
}

// EventTailOutput is a slice of a session's event log.
type EventTailOutput struct {
	Events []EventItem `json:"events"`
}

// EventItem is one persisted session event.
type EventItem struct {
	Seq  int64          `json:"seq"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func registerEventTail(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_event_tail",
		Description: "Read a FleetDeck session's persisted events for gap-filling a session view.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in EventTailInput) (*mcp.CallToolResult, EventTailOutput, error) {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, EventTailOutput{}, errors.New("session_id is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		events, err := hub.ListEvents(ctx, in.SessionID, in.AfterSeq, limit)
		if err != nil {
			return nil, EventTailOutput{}, err
		}
		out := EventTailOutput{Events: make([]EventItem, 0, len(events))}
		for _, e := range events {
			item := EventItem{Seq: e.Seq, Type: e.Type, Data: map[string]any{}}
			if len(e.Payload) > 0 {
				var data map[string]any
				if err := json.Unmarshal(e.Payload, &data); err == nil {
					item.Data = data
				}
			}
			out.Events = append(out.Events, item)
		}
		return nil, out, nil
	})
}

// ---- fleetdeck_session_send_message ----------------------------------------

// SendMessageInput is the argument set of fleetdeck_session_send_message.
type SendMessageInput struct {
	SessionID string `json:"session_id" jsonschema:"FleetDeck session id to resume"`
	Message   string `json:"message" jsonschema:"free-text reply to deliver to the session's agent"`
}

// SendMessageOutput is the outcome of the resume request.
type SendMessageOutput struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
}

func registerSendMessage(s *mcp.Server, hub *HubClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleetdeck_session_send_message",
		Description: "Send free-text to a FleetDeck session: reply to an awaiting_user session, steer a running one (the current turn is interrupted), or resume a finished one. The only mutating tool.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SendMessageInput) (*mcp.CallToolResult, SendMessageOutput, error) {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, SendMessageOutput{}, errors.New("session_id is required")
		}
		if strings.TrimSpace(in.Message) == "" {
			return nil, SendMessageOutput{}, errors.New("message is required")
		}
		res, err := hub.SendMessage(ctx, in.SessionID, in.Message)
		if err != nil {
			return nil, SendMessageOutput{}, err
		}
		status := "running"
		if res.Queued {
			status = "queued"
		}
		return nil, SendMessageOutput{OK: true, Status: status}, nil
	})
}
