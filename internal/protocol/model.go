// Package protocol is the single source of truth for the wire contract
// shared by the hub, nodes, and the web UI. Go structs here are exported to
// JSON Schema via `fleetctl schema export`, and the web generates TypeScript
// types from that schema. Do not define message shapes anywhere else.
package protocol

import "time"

// ---- Status enums ---------------------------------------------------------

// TaskStatus is the fixed 4-state lifecycle of a kanban task.
type TaskStatus string

const (
	TaskWaiting      TaskStatus = "waiting"
	TaskRunning      TaskStatus = "running"
	TaskDone         TaskStatus = "done"
	TaskAwaitingUser TaskStatus = "awaiting_user"
)

// SessionStatus mirrors the state of the underlying agent session.
type SessionStatus string

const (
	SessionWaiting      SessionStatus = "waiting"
	SessionRunning      SessionStatus = "running"
	SessionDone         SessionStatus = "done"
	SessionFailed       SessionStatus = "failed"
	SessionAwaitingUser SessionStatus = "awaiting_user"
)

// NodeStatus is the connectivity state of a node daemon.
type NodeStatus string

const (
	NodeOnline  NodeStatus = "online"
	NodeOffline NodeStatus = "offline"
)

// SubtaskStatus mirrors an agent todo item's completion state.
type SubtaskStatus string

const (
	SubtaskPending    SubtaskStatus = "pending"
	SubtaskInProgress SubtaskStatus = "in_progress"
	SubtaskDone       SubtaskStatus = "done"
)

// MessageRole is the author of a session message.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
)

// ---- Entities (DB-backed) ------------------------------------------------

// Node is a machine running a FleetDeck node daemon.
type Node struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Host         string     `json:"host,omitempty"`
	Status       NodeStatus `json:"status" jsonschema:"enum=online,enum=offline"`
	Capabilities []string   `json:"capabilities,omitempty"`
	LastSeenAt   time.Time  `json:"last_seen_at,omitempty"`
}

// Workspace is a working directory on a node, rendered as a kanban board (1:1).
type Workspace struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

// Task is a single agent session on a board.
type Task struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Title       string     `json:"title"`
	Prompt      string     `json:"prompt,omitempty"`
	Status      TaskStatus `json:"status" jsonschema:"enum=waiting,enum=running,enum=done,enum=awaiting_user"`
	AgentType   string     `json:"agent_type"`
	SessionID   string     `json:"session_id,omitempty"`
	SortOrder   int        `json:"sort_order"`
	// AutoStart is false for backlog tasks: created but never dispatched
	// until someone starts them explicitly.
	AutoStart bool `json:"auto_start"`
	// ArchivedAt marks a finished task hidden from the default board view.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// CancelledAt records the first manual termination, retained across resumes.
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Subtask is a task's sub-step, mirrored from the agent session's todo list.
type Subtask struct {
	ID         string        `json:"id"`
	TaskID     string        `json:"task_id"`
	Title      string        `json:"title"`
	Status     SubtaskStatus `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=done"`
	ExternalID string        `json:"external_id,omitempty"`
	SortOrder  int           `json:"sort_order"`
}

// Session is the agent session backing a task.
type Session struct {
	ID                string        `json:"id"`
	TaskID            string        `json:"task_id"`
	NodeID            string        `json:"node_id"`
	AgentType         string        `json:"agent_type"`
	ExternalSessionID string        `json:"external_session_id,omitempty"`
	Status            SessionStatus `json:"status" jsonschema:"enum=waiting,enum=running,enum=done,enum=failed,enum=awaiting_user"`
	CreatedAt         time.Time     `json:"created_at"`
	EndedAt           *time.Time    `json:"ended_at,omitempty"`
}

// Message is one line in a session's chat history.
type Message struct {
	ID        string      `json:"id"`
	SessionID string      `json:"session_id"`
	Role      MessageRole `json:"role" jsonschema:"enum=user,enum=assistant,enum=system"`
	Content   string      `json:"content"`
	CreatedAt time.Time   `json:"created_at"`
}

// ---- Token usage stats (GET /api/stats/tokens) -----------------------------

// TokenUsageTotals aggregates token counts across every matching usage row.
type TokenUsageTotals struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// TokenUsageByModel aggregates token counts for one model. Turns counts the
// underlying usage rows (one row per reported turn).
type TokenUsageByModel struct {
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Turns            int64  `json:"turns"`
}

// TokenUsageStats is the GET /api/stats/tokens response: the aggregate over
// all usage rows matching the query filters, plus a per-model breakdown
// sorted by total_tokens descending.
type TokenUsageStats struct {
	Total   TokenUsageTotals    `json:"total"`
	ByModel []TokenUsageByModel `json:"by_model"`
}

// ---- Task stats (GET /api/stats/tasks) -------------------------------------

// TaskStatsStatusCount buckets the tasks created in the reporting window by
// their current status.
type TaskStatsStatusCount struct {
	Status TaskStatus `json:"status" jsonschema:"enum=waiting,enum=running,enum=done,enum=awaiting_user"`
	Count  int64      `json:"count"`
}

// TaskStats is the GET /api/stats/tasks response over the query filters:
// Created counts tasks opened in the window, ByStatus splits those tasks by
// their current status, and Completed counts tasks that reached `done` with a
// last update inside the window (a later reopen moves them out again).
type TaskStats struct {
	Created   int64                  `json:"created"`
	Completed int64                  `json:"completed"`
	ByStatus  []TaskStatsStatusCount `json:"by_status"`
}
