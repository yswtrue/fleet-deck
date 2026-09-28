package protocol

import "time"

// ---- Event envelope -------------------------------------------------------

// Event is the WebSocket push envelope sent from the hub to the web UI.
// Type is a string discriminator (e.g. "task.status_changed"); Data carries the
// concrete payload. Concrete payload types are defined below.
//
// Seq (this envelope) and the per-session sequence persisted on the event table
// are distinct; see the Seq field comment.
type Event struct {
	Type string    `json:"type"`
	Data any       `json:"data"`
	Ts   time.Time `json:"ts"`

	// Seq is the hub-wide broadcast ordinal (monotonically increasing across all
	// events). The web uses it to detect gaps in the live WS stream. For
	// session-scoped gap-fill, query /api/sessions/{id}/events which returns
	// store.Event.Seq — a per-session sequence, distinct from this envelope seq.
	Seq int64 `json:"seq,omitempty"`
}

// Event type string constants (hub -> web).
const (
	EvNodeStatusChanged       = "node.status_changed"
	EvTaskCreated             = "task.created"
	EvTaskStatusChanged       = "task.status_changed"
	EvSubtaskUpdated          = "subtask.updated"
	EvSessionDelta            = "session.delta"
	EvSessionMessage          = "session.message"
	EvSessionStateChanged     = "session.state_changed"
	EvSessionUserInputRequest = "session.user_input_requested"
)

// ---- Hub -> Web event payloads -------------------------------------------

// NodeStatusChanged reports a node going online/offline.
type NodeStatusChanged struct {
	NodeID string     `json:"node_id"`
	Status NodeStatus `json:"status" jsonschema:"enum=online,enum=offline"`
}

// TaskCreated reports a newly created task.
type TaskCreated struct {
	Task Task `json:"task"`
}

// TaskStatusChanged reports a task's state transition.
type TaskStatusChanged struct {
	TaskID string     `json:"task_id"`
	From   TaskStatus `json:"from" jsonschema:"enum=waiting,enum=running,enum=done,enum=awaiting_user"`
	To     TaskStatus `json:"to" jsonschema:"enum=waiting,enum=running,enum=done,enum=awaiting_user"`
}

// SubtaskUpdated reports a subtask being created, updated, or removed. When
// Removed is true the hub deletes the subtask identified by Subtask.ID; when
// false the subtask is upserted by external_id (or by ID for the first time).
type SubtaskUpdated struct {
	TaskID  string  `json:"task_id"`
	Subtask Subtask `json:"subtask"`
	Removed bool    `json:"removed,omitempty"`
}

// SessionDelta is an incremental streamed text chunk from the agent.
type SessionDelta struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// SessionMessage is a complete message appended to the chat history.
type SessionMessage struct {
	SessionID string  `json:"session_id"`
	Message   Message `json:"message"`
}

// SessionStateChanged reports a session's lifecycle state change.
type SessionStateChanged struct {
	SessionID string        `json:"session_id"`
	Status    SessionStatus `json:"status" jsonschema:"enum=waiting,enum=running,enum=done,enum=failed,enum=awaiting_user"`
}

// SessionUserInputRequested is emitted when the agent pauses and asks the user
// for input (the task moves to awaiting_user).
type SessionUserInputRequested struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

// ---- Hub <-> Node messages ------------------------------------------------

// RegisterRequest is sent by a node when it first connects to the hub.
type RegisterRequest struct {
	Name         string   `json:"name"`
	Host         string   `json:"host,omitempty"`
	Capabilities []string `json:"capabilities"`
	Token        string   `json:"token,omitempty"`
}

// RegisterResponse acknowledges registration and issues a node id/token.
type RegisterResponse struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token,omitempty"`
}

// ---- Hub -> Node commands -------------------------------------------------

// TaskStartCommand tells a node to launch an agent session for a task.
type TaskStartCommand struct {
	TaskID        string `json:"task_id"`
	SessionID     string `json:"session_id"`
	AgentType     string `json:"agent_type"`
	Prompt        string `json:"prompt"`
	WorkspacePath string `json:"workspace_path"`
}

// SendMessageCommand resumes an awaiting_user session with the user's reply.
type SendMessageCommand struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// SessionResumeCommand revives a finished (done/failed) session with the
// user's follow-up: the node reattaches to the agent-side session via
// session/load and then prompts with Message. ExternalSessionID is the
// agent-side session id recorded when the session first ran.
type SessionResumeCommand struct {
	SessionID         string `json:"session_id"`
	TaskID            string `json:"task_id"`
	AgentType         string `json:"agent_type"`
	WorkspacePath     string `json:"workspace_path"`
	ExternalSessionID string `json:"external_session_id"`
	Message           string `json:"message"`
}

// CancelCommand terminates a running session.
type CancelCommand struct {
	SessionID string `json:"session_id"`
}

// ---- Node -> Hub events (reported up) ------------------------------------

// Todo is one item in the agent's todo list, mirrored to a Subtask.
type Todo struct {
	ID     string        `json:"id"`
	Title  string        `json:"title"`
	Status SubtaskStatus `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=done"`
}

// TodoUpdate is the full todo list snapshot for a session after a change.
type TodoUpdate struct {
	SessionID string `json:"session_id"`
	Todos     []Todo `json:"todos"`
}
