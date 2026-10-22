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
	EvSessionThought          = "session.thought"
	EvSessionMessage          = "session.message"
	EvSessionStateChanged     = "session.state_changed"
	EvSessionUserInputRequest = "session.user_input_requested"
	EvSessionUsage            = "session.usage"
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

// SessionThought is a chunk of the agent's reasoning stream. It is live-only:
// reasoning is content, so the hub broadcasts it without persisting it or
// buffering it (a reload drops the partial trace).
type SessionThought struct {
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

// SessionUsage reports one turn's model token usage. The node relays what the
// agent reported; the hub persists it as a token_usage row. A session with no
// reported usage produces no rows — numbers are never fabricated.
type SessionUsage struct {
	SessionID        string `json:"session_id"`
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// ---- Hub <-> Node messages ------------------------------------------------

// RegisterRequest is sent by a node when it first connects to the hub.
type RegisterRequest struct {
	Name         string   `json:"name"`
	Host         string   `json:"host,omitempty"`
	Capabilities []string `json:"capabilities"`
	// Token is the node credential issued by a previous registration.
	Token string `json:"token,omitempty"`
	// JoinToken is the single-use pairing token required for a node that has
	// never registered. It is ignored once the node exists (Token authenticates
	// reconnects).
	JoinToken string `json:"join_token,omitempty"`
	// MachineID is the node's stable machine fingerprint (already hashed by the
	// daemon). The hub allows at most one node per fingerprint, so a second
	// daemon on the same computer cannot register under another name.
	MachineID string `json:"machine_id,omitempty"`
}

// RegisterResponse acknowledges registration and issues a node id/token.
// Error is set when the hub rejects the handshake (e.g. a missing or spent
// join token); the hub closes the socket after sending it.
type RegisterResponse struct {
	NodeID string `json:"node_id,omitempty"`
	Token  string `json:"token,omitempty"`
	Error  string `json:"error,omitempty"`
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

// SessionStarted reports the agent-side session id an adapter obtained when it
// opened (session/new) or reattached (session/load) a session. The hub stores
// it so GET /api/sessions/{id}/messages can replay the conversation through the
// owning node after the session finishes: message content is never persisted
// server-side.
type SessionStarted struct {
	SessionID         string `json:"session_id"`
	ExternalSessionID string `json:"external_session_id"`
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

// SessionHistoryRequest asks a node to replay a session's conversation from
// the agent's own session store. The hub proxies it for
// GET /api/sessions/{id}/messages: session content is deliberately never
// persisted server-side (privacy model — the hub stores task metadata and
// status events only), so history is recovered on demand. ReqID correlates
// the node's MsgSessionHistoryResp.
type SessionHistoryRequest struct {
	ReqID             string `json:"req_id"`
	SessionID         string `json:"session_id"`
	AgentType         string `json:"agent_type"`
	ExternalSessionID string `json:"external_session_id"`
	WorkspacePath     string `json:"workspace_path"`
}

// SessionHistoryResponse carries the replayed conversation (oldest first) or
// an Error when the node cannot replay: an adapter without history support, a
// missing agent-side session, or a replay failure. On error Messages is empty.
type SessionHistoryResponse struct {
	ReqID     string    `json:"req_id"`
	SessionID string    `json:"session_id"`
	Messages  []Message `json:"messages"`
	Error     string    `json:"error,omitempty"`
}

// FSListRequest asks a node to enumerate the directories under Path so the
// web UI can offer a folder picker instead of a blind absolute path. An empty
// Path means the node's home directory. ReqID correlates MsgFSListResp.
type FSListRequest struct {
	ReqID string `json:"req_id"`
	Path  string `json:"path,omitempty"`
}

// FSDirEntry is one directory inside a listed path. Only directories are
// reported: workspace paths must be directories, so file names are noise.
type FSDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FSListResponse carries one directory listing. Path is the absolute,
// symlink-resolved directory that was listed and Parent its parent (empty at
// a filesystem root). Error is set when the node could not read the path.
type FSListResponse struct {
	ReqID   string       `json:"req_id"`
	Path    string       `json:"path,omitempty"`
	Parent  string       `json:"parent,omitempty"`
	Entries []FSDirEntry `json:"entries"`
	Error   string       `json:"error,omitempty"`
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
