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
