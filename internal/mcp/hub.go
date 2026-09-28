package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fleetdeck/internal/protocol"
)

// requestTimeout bounds every hub round trip so a hung hub cannot wedge a tool
// call forever. The MCP client can already cancel via ctx; this is a backstop.
const requestTimeout = 15 * time.Second

// maxResponseBytes caps how much of a hub response is read into memory.
const maxResponseBytes = 4 << 20

// HubClient is a thin HTTP client over the hub's REST API. The MCP server talks
// to the hub rather than Postgres so it inherits the hub's single-writer
// invariants and needs no database credentials.
type HubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewHubClient builds a client for baseURL. A trailing slash is tolerated.
func NewHubClient(baseURL string) *HubClient {
	return NewHubClientWithToken(baseURL, "")
}

// NewHubClientWithToken builds a client that sends
// "Authorization: Bearer <token>" on every hub request. An empty token sends
// no Authorization header; the hub then answers 401 on every protected
// endpoint.
func NewHubClientWithToken(baseURL, token string) *HubClient {
	return &HubClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// setAuth stamps the bearer credential onto an outgoing hub request.
func (c *HubClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// HubError is a non-2xx hub response. Tool handlers surface it as a structured
// tool error result instead of crashing.
type HubError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HubError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	if body == "" {
		return fmt.Sprintf("hub returned %s", e.Status)
	}
	return fmt.Sprintf("hub returned %s: %s", e.Status, body)
}

// SessionSnapshot mirrors GET /api/sessions/{id}: the session plus its parent
// task, which is null when the task cannot be resolved.
type SessionSnapshot struct {
	Session protocol.Session `json:"session"`
	Task    *protocol.Task   `json:"task"`
}

// Event mirrors the hub's eventDTO: the stored JSONB payload is returned raw.
type Event struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Seq       int64           `json:"seq"`
	Ts        time.Time       `json:"ts"`
}

// SendResult is the outcome of POST /api/sessions/{id}/send.
type SendResult struct {
	// StatusCode is the raw HTTP status (200 when delivered, 202 when the node
	// is offline and the message was queued).
	StatusCode int
	// Queued is true when the hub accepted the message but the owning node was
	// offline.
	Queued bool
	// Message is the persisted user message (zero value when queued).
	Message protocol.Message
}

func (c *HubClient) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build hub request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hub request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read hub response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HubError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}

// GetSession returns a session and its parent task in one round trip.
func (c *HubClient) GetSession(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	var snap SessionSnapshot
	path := "/api/sessions/" + url.PathEscape(sessionID)
	if err := c.get(ctx, path, nil, &snap); err != nil {
		return SessionSnapshot{}, err
	}
	return snap, nil
}

// ListTasksByWorkspace returns the tasks on a workspace's board.
func (c *HubClient) ListTasksByWorkspace(ctx context.Context, workspaceID string) ([]protocol.Task, error) {
	q := url.Values{"workspace_id": {workspaceID}}
	var tasks []protocol.Task
	if err := c.get(ctx, "/api/tasks", q, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListTasksByNode returns every task across a node's workspaces in one
// round trip (GET /api/tasks?node_id=).
func (c *HubClient) ListTasksByNode(ctx context.Context, nodeID string) ([]protocol.Task, error) {
	q := url.Values{"node_id": {nodeID}}
	var tasks []protocol.Task
	if err := c.get(ctx, "/api/tasks", q, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListSubtasks returns a task's mirrored todo items via
// GET /api/tasks/{id}/subtasks, which responds with a {"subtasks":[...]}
// envelope.
func (c *HubClient) ListSubtasks(ctx context.Context, taskID string) ([]protocol.Subtask, error) {
	var body struct {
		Subtasks []protocol.Subtask `json:"subtasks"`
	}
	path := "/api/tasks/" + url.PathEscape(taskID) + "/subtasks"
	if err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Subtasks, nil
}

// ListMessages returns the most recent messages of a session, oldest first.
func (c *HubClient) ListMessages(ctx context.Context, sessionID string, limit int) ([]protocol.Message, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var messages []protocol.Message
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := c.get(ctx, path, q, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// ListEvents returns session events with a per-session seq greater than
// afterSeq, for gap-filling a session view.
func (c *HubClient) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, error) {
	q := url.Values{}
	if afterSeq > 0 {
		q.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var events []Event
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/events"
	if err := c.get(ctx, path, q, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// SendMessage records a user reply on an awaiting_user session and resumes it.
// It is the only mutating call the MCP server exposes.
func (c *HubClient) SendMessage(ctx context.Context, sessionID, message string) (SendResult, error) {
	payload, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return SendResult{}, fmt.Errorf("encode send payload: %w", err)
	}

	path := "/api/sessions/" + url.PathEscape(sessionID) + "/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return SendResult{}, fmt.Errorf("build hub request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return SendResult{}, fmt.Errorf("hub request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return SendResult{}, fmt.Errorf("read hub response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SendResult{}, &HubError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(body)}
	}

	var parsed struct {
		OK      bool             `json:"ok"`
		Status  string           `json:"status"`
		Message protocol.Message `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SendResult{}, fmt.Errorf("decode hub response: %w", err)
	}
	return SendResult{
		StatusCode: resp.StatusCode,
		Queued:     parsed.Status == "queued",
		Message:    parsed.Message,
	}, nil
}
