package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store"
)

// ---- helpers ---------------------------------------------------------------

// readEnvelopes drains a node connection into a channel, skipping nothing.
func readEnvelopes(c *websocket.Conn) <-chan protocol.Envelope {
	ch := make(chan protocol.Envelope, 16)
	go func() {
		defer close(ch)
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			select {
			case ch <- env:
			default: // drop rather than block the reader
			}
		}
	}()
	return ch
}

// waitEnvelope returns the first envelope of the given type, failing on timeout.
func waitEnvelope(t *testing.T, ch <-chan protocol.Envelope, typ string) protocol.Envelope {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatalf("node connection closed while waiting for %q", typ)
			}
			if env.Type == typ {
				return env
			}
		case <-timeout:
			t.Fatalf("timed out waiting for node envelope %q", typ)
		}
	}
}

// randomUUID returns a random RFC 4122 v4 UUID string.
func randomUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- tests -----------------------------------------------------------------

// TestSendMessageSuccess drives the happy path: an awaiting_user session gets a
// user reply that is persisted, resumes the session, broadcasts both events,
// and reaches the owning node.
func TestSendMessageSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("send-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionAwaitingUser, nil); err != nil {
		t.Fatalf("seed awaiting_user: %v", err)
	}

	events, unsubscribe := h.Bus().Subscribe(EventScope{All: true})
	defer unsubscribe()

	const reply = "please continue"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": reply})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		OK      bool             `json:"ok"`
		Message protocol.Message `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode send response: %v (%s)", err, raw)
	}
	if !body.OK {
		t.Fatalf("send response ok=false: %s", raw)
	}
	if body.Message.ID == "" || body.Message.Role != protocol.RoleUser || body.Message.Content != reply {
		t.Fatalf("returned message = %+v, want a buffered user message %q", body.Message, reply)
	}

	// Session resumed.
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q, want running", sess.Status)
	}

	// Message buffered (privacy model: content is never persisted server-side).
	msgs := h.textBuffer(ctx, task.SessionID).Messages
	var found bool
	for _, m := range msgs {
		if m.Role == protocol.RoleUser && m.Content == reply {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message %q not buffered; got %d buffered", reply, len(msgs))
	}

	// Both broadcasts emitted.
	var sawMessage, sawState bool
	timeout := time.After(3 * time.Second)
	for !sawMessage || !sawState {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("broadcaster closed unexpectedly")
			}
			switch evt.Type {
			case protocol.EvSessionMessage:
				if sm, ok := evt.Data.(protocol.SessionMessage); ok &&
					sm.SessionID == task.SessionID && sm.Message.Role == protocol.RoleUser && sm.Message.Content == reply {
					sawMessage = true
				}
			case protocol.EvSessionStateChanged:
				if sc, ok := evt.Data.(protocol.SessionStateChanged); ok &&
					sc.SessionID == task.SessionID && sc.Status == protocol.SessionRunning {
					sawState = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for broadcasts: sawMessage=%v sawState=%v", sawMessage, sawState)
		}
	}

	// Envelope delivered to the node.
	env := waitEnvelope(t, envelopes, protocol.MsgSendMessage)
	var cmd protocol.SendMessageCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode send_message command: %v", err)
	}
	if cmd.SessionID != task.SessionID || cmd.Message != reply {
		t.Fatalf("node command = %+v, want session %q message %q", cmd, task.SessionID, reply)
	}
}

// TestSendMessageRefusesWhenNotAwaiting asserts a running session is rejected
// with 409 and left untouched.
func TestSendMessageRefusesWhenNotAwaiting(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("noawait-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": "hello"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("send status = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode 409 body: %v (%s)", err, raw)
	}
	if body.Error != "session is running" || body.Status != string(protocol.SessionRunning) {
		t.Fatalf("409 body = %+v, want error=session is running status=running", body)
	}

	ctx := context.Background()
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q (err %v), want running", sess.Status, err)
	}
	// The dispatch prompt is the first transcript line; the rejected reply must
	// not be buffered.
	for _, m := range h.textBuffer(ctx, task.SessionID).Messages {
		if m.Role == protocol.RoleUser && m.Content != "do the thing" {
			t.Fatalf("unexpected buffered user message: %+v", m)
		}
	}
}

// TestSendMessageRejectsEmpty asserts blank messages are rejected before any
// session lookup happens.
func TestSendMessageRejectsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	for _, body := range []string{"", "   "} {
		resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/whatever/send", map[string]string{"message": body})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("send status = %d, want 400; body=%s", resp.StatusCode, raw)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode 400 body: %v (%s)", err, raw)
		}
		if errBody.Error != "message is required" {
			t.Fatalf("400 body = %+v, want error=message is required", errBody)
		}
	}
}

// TestSendMessageUnknownSession asserts an unknown session id yields 404.
func TestSendMessageUnknownSession(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/00000000-0000-0000-0000-000000000000/send", map[string]string{"message": "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("send status = %d, want 404; body=%s", resp.StatusCode, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode 404 body: %v (%s)", err, raw)
	}
	if errBody.Error != "session not found" {
		t.Fatalf("404 body = %+v, want error=session not found", errBody)
	}
}

// TestSendMessageNodeOffline asserts an offline node does not fail the request:
// the message is stored and the session resumes, but the caller gets 202 with a
// warning and no envelope is delivered.
func TestSendMessageNodeOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("offline-send-node"))

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	// Drop the connection and wait for the hub to notice and finish its
	// offline cascade (which fails the running session).
	_ = c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for h.nodeOnline(nodeID) {
		if time.Now().After(deadline) {
			t.Fatal("node still online after close")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		sess, err := st.Queries().GetSession(ctx, task.SessionID)
		if err == nil && sess.Status == protocol.SessionFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offline cascade did not fail the session in time")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Re-arm the session for the send path.
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionAwaitingUser, nil); err != nil {
		t.Fatalf("seed awaiting_user: %v", err)
	}

	const reply = "retry after offline"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": reply})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d, want 202; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		Status  string `json:"status"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode 202 body: %v (%s)", err, raw)
	}
	if body.Status != "queued" || body.Warning == "" {
		t.Fatalf("202 body = %+v, want status=queued with a warning", body)
	}

	// The message is buffered (not persisted) and the session resumed even
	// while the node is unreachable.
	var found bool
	for _, m := range h.textBuffer(ctx, task.SessionID).Messages {
		if m.Role == protocol.RoleUser && m.Content == reply {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message %q not buffered while node offline", reply)
	}
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q (err %v), want running", sess.Status, err)
	}
}

// TestSendMessageBumpsTaskToRunning asserts the locked Gate 2 decision: a user
// reply resumes the parent task awaiting_user -> running and broadcasts the
// task status change alongside the session events.
func TestSendMessageBumpsTaskToRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("bump-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	// The fake adapter is happy-path only, so seed both rows into awaiting_user
	// directly through the store (this deliberately bypasses the hub state
	// machine to set up the awaiting_user state under test).
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionAwaitingUser, nil); err != nil {
		t.Fatalf("seed session awaiting_user: %v", err)
	}
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskAwaitingUser); err != nil {
		t.Fatalf("seed task awaiting_user: %v", err)
	}

	events, unsubscribe := h.Bus().Subscribe(EventScope{All: true})
	defer unsubscribe()

	const reply = "carry on"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": reply})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// The task resumed in lock-step with the session.
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != protocol.TaskRunning {
		t.Fatalf("task status = %q, want running", got.Status)
	}

	// Both the task bump and the session resume are broadcast.
	var sawTaskBump, sawSessionRunning bool
	timeout := time.After(3 * time.Second)
	for !sawTaskBump || !sawSessionRunning {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("broadcaster closed unexpectedly")
			}
			switch evt.Type {
			case protocol.EvTaskStatusChanged:
				if tc, ok := evt.Data.(protocol.TaskStatusChanged); ok &&
					tc.TaskID == task.ID && tc.From == protocol.TaskAwaitingUser && tc.To == protocol.TaskRunning {
					sawTaskBump = true
				}
			case protocol.EvSessionStateChanged:
				if sc, ok := evt.Data.(protocol.SessionStateChanged); ok &&
					sc.SessionID == task.SessionID && sc.Status == protocol.SessionRunning {
					sawSessionRunning = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for broadcasts: sawTaskBump=%v sawSessionRunning=%v", sawTaskBump, sawSessionRunning)
		}
	}
}

// TestGetSessionHappy asserts GET /api/sessions/{id} returns the session plus its
// parent task, and nothing else.
func TestGetSessionHappy(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("get-session-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	var got struct {
		Session *protocol.Session `json:"session"`
		Task    *protocol.Task    `json:"task"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode body: %v (%s)", err, raw)
	}
	if got.Session == nil || got.Session.ID != task.SessionID || got.Session.TaskID != task.ID {
		t.Fatalf("session = %+v, want id=%q task_id=%q", got.Session, task.SessionID, task.ID)
	}
	if got.Task == nil || got.Task.ID != task.ID {
		t.Fatalf("task = %+v, want id=%q", got.Task, task.ID)
	}

	// The body must carry exactly the SessionSnapshot fields: session, task,
	// and the pending ask's prompt (content served from the ephemeral buffer).
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode raw keys: %v (%s)", err, raw)
	}
	if len(keys) != 3 || keys["session"] == nil || keys["task"] == nil || keys["pending_input"] == nil {
		t.Fatalf("response keys = %v, want exactly session+task+pending_input", keys)
	}
}

// TestGetSessionNotFound asserts an unknown session id yields 404 with the
// shared error body.
func TestGetSessionNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+randomUUID(t))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get session status = %d, want 404; body=%s", resp.StatusCode, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode 404 body: %v (%s)", err, raw)
	}
	if errBody.Error != "session not found" {
		t.Fatalf("404 body = %+v, want error=session not found", errBody)
	}
}

// TestGetSessionTaskMissing covers the defensive branch where the parent task
// cannot be resolved: the session is still returned and task is null. Because
// session.task_id is ON DELETE CASCADE, deleting the task would also delete the
// session, so the FK is dropped for the duration of the test to simulate an
// orphaned session, then restored (the orphan session is removed first so the
// constraint re-validates cleanly).
func TestGetSessionTaskMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("missing-task-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	ctx := context.Background()
	pool := st.Pool()

	if _, err := pool.Exec(ctx, "ALTER TABLE session DROP CONSTRAINT IF EXISTS session_task_id_fkey"); err != nil {
		t.Fatalf("drop session FK: %v", err)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(bg, "DELETE FROM session WHERE id = $1", task.SessionID); err != nil {
			t.Logf("cleanup: delete orphan session: %v", err)
		}
		if _, err := pool.Exec(bg, "ALTER TABLE session ADD CONSTRAINT session_task_id_fkey FOREIGN KEY (task_id) REFERENCES task (id) ON DELETE CASCADE"); err != nil {
			t.Logf("cleanup: restore session FK: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, "DELETE FROM task WHERE id = $1", task.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var got struct {
		Session *protocol.Session `json:"session"`
		Task    *protocol.Task    `json:"task"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode body: %v (%s)", err, raw)
	}
	if got.Session == nil || got.Session.ID != task.SessionID {
		t.Fatalf("session = %+v, want id %q", got.Session, task.SessionID)
	}
	if got.Task != nil {
		t.Fatalf("task = %+v, want null", got.Task)
	}
	if !bytes.Contains(raw, []byte(`"task":null`)) {
		t.Fatalf("body missing task:null: %s", raw)
	}
}

// ---- subtask listing -------------------------------------------------------

// TestListSubtasksByTaskHappy seeds three subtasks and asserts
// GET /api/tasks/{id}/subtasks returns them in sort order under a single
// "subtasks" key.
func TestListSubtasksByTaskHappy(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("subtask-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)

	ctx := context.Background()
	want := []struct {
		external string
		title    string
		status   protocol.SubtaskStatus
	}{
		{"todo-1", "Research", protocol.SubtaskDone},
		{"todo-2", "Implement", protocol.SubtaskInProgress},
		{"todo-3", "Ship", protocol.SubtaskPending},
	}
	for i, w := range want {
		if _, err := st.Queries().UpsertSubtaskByExternalID(ctx, store.UpsertSubtaskByExternalIDParams{
			TaskID:     task.ID,
			Title:      w.title,
			Status:     w.status,
			ExternalID: w.external,
			SortOrder:  i,
		}); err != nil {
			t.Fatalf("seed subtask %d: %v", i, err)
		}
	}

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID+"/subtasks")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	var body struct {
		Subtasks []protocol.Subtask `json:"subtasks"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, raw)
	}
	if len(body.Subtasks) != len(want) {
		t.Fatalf("got %d subtasks, want %d: %+v", len(body.Subtasks), len(want), body.Subtasks)
	}
	for i, w := range want {
		got := body.Subtasks[i]
		if got.TaskID != task.ID || got.ExternalID != w.external || got.Title != w.title || got.Status != w.status {
			t.Fatalf("subtask[%d] = %+v, want task=%q external=%q title=%q status=%q",
				i, got, task.ID, w.external, w.title, w.status)
		}
		if got.ID == "" {
			t.Fatalf("subtask[%d] has no id: %+v", i, got)
		}
	}

	// Array shape: exactly one "subtasks" key, and it is a JSON array.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode raw keys: %v (%s)", err, raw)
	}
	if len(keys) != 1 || keys["subtasks"] == nil {
		t.Fatalf("response keys = %v, want exactly subtasks", keys)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(keys["subtasks"]), []byte("[")) {
		t.Fatalf("subtasks is not an array: %s", keys["subtasks"])
	}
}

// TestListSubtasksByTaskEmpty asserts a task with no mirrored todos returns an
// empty array (never null).
func TestListSubtasksByTaskEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("subtask-empty-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID+"/subtasks")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	var body struct {
		Subtasks []protocol.Subtask `json:"subtasks"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, raw)
	}
	if body.Subtasks == nil || len(body.Subtasks) != 0 {
		t.Fatalf("subtasks = %#v, want a non-nil empty slice", body.Subtasks)
	}
	if !bytes.Contains(raw, []byte(`"subtasks":[]`)) {
		t.Fatalf("body = %s, want {\"subtasks\":[]}", raw)
	}
}

// TestListSubtasksByTaskInvalidID asserts a non-UUID path value is rejected
// with 400 before touching the database.
func TestListSubtasksByTaskInvalidID(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/tasks/not-a-valid-uuid/subtasks")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode 400 body: %v (%s)", err, raw)
	}
	if errBody.Error != "invalid task id" {
		t.Fatalf("400 body = %+v, want error=invalid task id", errBody)
	}
}

// ---- CORS (web UI runs on another localhost port) --------------------------

// TestCORSHeaders pins the Access-Control-Allow-Origin header: without it the
// browser blocks every API call from the vite dev server origin.
func TestCORSHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get health status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

// TestCORSPreflight pins the preflight answer: the web API client sends
// Content-Type on every request, so browsers must preflight cross-origin.
func TestCORSPreflight(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, _ := newTestHub(t)

	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/api/tasks", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "http://127.0.0.1:7711")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /api/tasks: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET, POST, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type, Authorization",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

// ---- GET /api/tasks filters ------------------------------------------------

// TestListTasksByNodeFilter asserts node_id spans every workspace on the node
// (one round trip, the collapsed N+1) and stays strictly scoped to that node.
func TestListTasksByNodeFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	ca, nodeA := dialNode(t, ts, st, uniqueName("hub-it-nodea"))
	cb, nodeB := dialNode(t, ts, st, uniqueName("hub-it-nodeb"))
	defer func() { _ = ca.Close(); _ = cb.Close() }()

	ws1 := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeA, "name": uniqueName("it-ws"), "path": "/tmp/it-ws",
	})
	ws2 := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeA, "name": uniqueName("it-ws"), "path": "/tmp/it-ws",
	})
	task1 := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws1.ID, "title": uniqueName("it-task"), "prompt": "p", "agent_type": "fake",
	})
	task2 := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": ws2.ID, "title": uniqueName("it-task"), "prompt": "p", "agent_type": "fake",
	})
	taskB := createWorkspaceAndTask(t, auth.token, ts, nodeB)

	decodeTasks := func(raw []byte) map[string]bool {
		var tasks []protocol.Task
		if err := json.Unmarshal(raw, &tasks); err != nil {
			t.Fatalf("decode tasks: %v (%s)", err, raw)
		}
		ids := make(map[string]bool, len(tasks))
		for _, task := range tasks {
			ids[task.ID] = true
		}
		return ids
	}

	respA, rawA := getRaw(t, auth.token, ts.URL+"/api/tasks?node_id="+nodeA)
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("list by node status = %d, body=%s", respA.StatusCode, rawA)
	}
	idsA := decodeTasks(rawA)
	if len(idsA) != 2 || !idsA[task1.ID] || !idsA[task2.ID] {
		t.Fatalf("tasks for node A = %v, want {%s %s}", idsA, task1.ID, task2.ID)
	}

	_, rawB := getRaw(t, auth.token, ts.URL+"/api/tasks?node_id="+nodeB)
	idsB := decodeTasks(rawB)
	if len(idsB) != 1 || !idsB[taskB.ID] {
		t.Fatalf("tasks for node B = %v, want {%s}", idsB, taskB.ID)
	}

	_, rawW := getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws1.ID)
	idsW := decodeTasks(rawW)
	if len(idsW) != 1 || !idsW[task1.ID] {
		t.Fatalf("tasks for ws1 = %v, want {%s}", idsW, task1.ID)
	}
}

// TestListTasksFilterValidation pins the one-filter contract: exactly one of
// workspace_id / node_id.
func TestListTasksFilterValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, _, ts, auth := newTestHub(t)

	for _, tc := range []struct {
		query   string
		wantMsg string
	}{
		{"", "workspace_id or node_id is required"},
		{"?workspace_id=ws&node_id=n", "provide only one of workspace_id or node_id"},
	} {
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/tasks"+tc.query)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET /api/tasks%s status = %d, want 400; body=%s", tc.query, resp.StatusCode, raw)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode 400 body: %v (%s)", err, raw)
		}
		if errBody.Error != tc.wantMsg {
			t.Fatalf("GET /api/tasks%s error = %q, want %q", tc.query, errBody.Error, tc.wantMsg)
		}
	}
}

// ---- task title fuzzy search (pg_trgm) --------------------------------------

// TestSearchTasksByTitle pins the search contract: substring matches, typo
// tolerant trigram matches, best-match-first ranking, and workspace/node
// scoping.
func TestSearchTasksByTitle(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-it-search"))
	defer func() { _ = c.Close() }()

	ws := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID, "name": uniqueName("it-ws"), "path": "/tmp/it-ws",
	})
	mk := func(title string) protocol.Task {
		return postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
			"workspace_id": ws.ID, "title": title, "prompt": "p", "agent_type": "fake",
		})
	}
	report := mk("Receive payment report")
	retry := mk("Payment retry logic")
	deploy := mk("Deploy kubernetes cluster")

	decodeIDs := func(raw []byte) []string {
		var tasks []protocol.Task
		if err := json.Unmarshal(raw, &tasks); err != nil {
			t.Fatalf("decode tasks: %v (%s)", err, raw)
		}
		ids := make([]string, 0, len(tasks))
		for _, task := range tasks {
			ids = append(ids, task.ID)
		}
		return ids
	}

	// Substring: both payment tasks, nothing else.
	_, raw := getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws.ID+"&q=payment")
	ids := decodeIDs(raw)
	if len(ids) != 2 || (ids[0] != report.ID && ids[1] != report.ID) || (ids[0] != retry.ID && ids[1] != retry.ID) {
		t.Fatalf("substring search ids = %v, want {%s %s}", ids, report.ID, retry.ID)
	}

	// Typo-tolerant: "recieve payment report" must find "Receive payment
	// report" via trigram similarity, and must not drag in the deploy task.
	_, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws.ID+"&q=recieve%20payment%20report")
	ids = decodeIDs(raw)
	found, leaked := false, false
	for _, id := range ids {
		if id == report.ID {
			found = true
		}
		if id == deploy.ID {
			leaked = true
		}
	}
	if !found {
		t.Fatalf("fuzzy search ids = %v, want it to contain %s", ids, report.ID)
	}
	if leaked {
		t.Fatalf("fuzzy search ids = %v, must not contain unrelated %s", ids, deploy.ID)
	}

	// Ranking: the closer title comes first.
	_, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws.ID+"&q=payment%20retry")
	ids = decodeIDs(raw)
	if len(ids) == 0 || ids[0] != retry.ID {
		t.Fatalf("ranked search ids = %v, want %s first", ids, retry.ID)
	}

	// Node scoping hits the same matcher across workspaces.
	_, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?node_id="+nodeID+"&q=payment")
	if ids := decodeIDs(raw); len(ids) != 2 {
		t.Fatalf("node-scoped search ids = %v, want 2 matches", ids)
	}

	// No match returns an empty list, not an error.
	_, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+ws.ID+"&q=nothing%20matches%20this")
	if ids := decodeIDs(raw); len(ids) != 0 {
		t.Fatalf("non-matching search ids = %v, want none", ids)
	}
}

// ---- PATCH /api/tasks/{id} transitions --------------------------------------

// TestPatchTaskTransitionValidation pins the manual transition table: waiting
// is start+archive only (dispatch owns waiting -> running), running may pause
// back to waiting or be marked done, awaiting_user is agent-triggered to enter
// but may resume or finish manually, done is terminal, same-status is
// idempotent, and illegal moves are 409.
func TestPatchTaskTransitionValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-it-patch"))
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	patchStatus := func(id, status string) (*http.Response, []byte) {
		return patchRaw(t, auth.token, ts.URL+"/api/tasks/"+id, map[string]any{"status": status})
	}

	if resp, raw := patchStatus(task.ID, "done"); resp.StatusCode != http.StatusOK {
		t.Fatalf("running -> done status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task.ID, "running"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("done -> running status = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task.ID, "done"); resp.StatusCode != http.StatusOK {
		t.Fatalf("done -> done (idempotent) status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// A task on an offline node is never dispatched: it really is waiting.
	offline := seedNode(t, st, uniqueName("it-offline"))
	offWS := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": offline.ID, "name": uniqueName("it-ws"), "path": "/tmp/it-ws",
	})
	task2 := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": offWS.ID, "title": uniqueName("it-task"), "prompt": "p", "agent_type": "fake",
	})
	if resp, raw := patchStatus(task2.ID, "running"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("waiting -> running status = %d, want 409 (dispatch owns it); body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task2.ID, "awaiting_user"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("waiting -> awaiting_user status = %d, want 409 (agent-triggered only); body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task2.ID, "done"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("waiting -> done status = %d, want 409; body=%s", resp.StatusCode, raw)
	}

	// Seed non-waiting states through the store: the manual table is the
	// system under test, not the dispatcher or the session derivation.
	if _, err := st.Queries().UpdateTaskStatus(ctx, task2.ID, protocol.TaskRunning); err != nil {
		t.Fatalf("seed task running: %v", err)
	}
	if resp, raw := patchStatus(task2.ID, "awaiting_user"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("running -> awaiting_user status = %d, want 409 (agent-triggered only); body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task2.ID, "waiting"); resp.StatusCode != http.StatusOK {
		t.Fatalf("running -> waiting (pause) status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	if _, err := st.Queries().UpdateTaskStatus(ctx, task2.ID, protocol.TaskAwaitingUser); err != nil {
		t.Fatalf("seed task awaiting_user: %v", err)
	}
	if resp, raw := patchStatus(task2.ID, "running"); resp.StatusCode != http.StatusOK {
		t.Fatalf("awaiting_user -> running status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
}

// TestGetNodeByID covers GET /api/nodes/{id}, the lookup behind the web UI's
// /nodes/:nodeId deep links: admins read every node, non-admins only their
// bound nodes (unbound -> 404), unknown ids are 404, and anonymous callers
// are 401.
func TestGetNodeByID(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, st, ts, auth := newTestHub(t)
	user, userToken := setupUserWithRole(t, st, "user")

	node := seedNode(t, st, uniqueName("it-node-get"))

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node (admin)", resp, raw, http.StatusOK)
	if got := decodeOK[protocol.Node](t, "get node", resp, raw); got.Name != node.Name {
		t.Fatalf("get node name = %q, want %q", got.Name, node.Name)
	}

	// Unbound non-admin: 404 (no existence leak).
	resp, raw = getRaw(t, userToken, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node (unbound user)", resp, raw, http.StatusNotFound)

	// Binding the user flips the node visible.
	bindUserToNodes(t, ts.URL, auth.token, user.ID, node.ID)
	resp, raw = getRaw(t, userToken, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node (bound user)", resp, raw, http.StatusOK)

	resp, raw = getRaw(t, auth.token, ts.URL+"/api/nodes/00000000-0000-0000-0000-000000000000")
	requireStatus(t, "get unknown node", resp, raw, http.StatusNotFound)

	resp, raw = getRaw(t, "", ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node without token", resp, raw, http.StatusUnauthorized)
}

// TestSendMessageResumesDoneSession asserts the resume path: a user message to
// a done session is persisted, the session flips back to running, the parent
// task reopens done -> running (bypassing the manual PATCH table), and the
// owning node receives a session.resume command carrying the recorded
// external session id.
func TestSendMessageResumesDoneSession(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("resume-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	// Finish the session the way the node would: failed and done both route
	// through syncTaskFromSession, driving the task to done.
	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("seed session done: %v", err)
	}
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskDone); err != nil {
		t.Fatalf("seed task done: %v", err)
	}
	const extID = "agent-resume-1"
	if err := st.Queries().UpdateSessionExternalID(ctx, task.SessionID, extID); err != nil {
		t.Fatalf("seed external id: %v", err)
	}

	events, unsubscribe := h.Bus().Subscribe(EventScope{All: true})
	defer unsubscribe()

	const followUp = "now do it again, better"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": followUp})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// Session running again, terminal marker cleared.
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q, want running", sess.Status)
	}
	if sess.EndedAt != nil {
		t.Fatalf("session ended_at = %v, want nil after resume", sess.EndedAt)
	}

	// Task reopened.
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != protocol.TaskRunning {
		t.Fatalf("task status = %q, want running", got.Status)
	}

	// Follow-up buffered as a user message (never persisted).
	var found bool
	for _, m := range h.textBuffer(ctx, task.SessionID).Messages {
		if m.Role == protocol.RoleUser && m.Content == followUp {
			found = true
		}
	}
	if !found {
		t.Fatalf("follow-up %q not buffered", followUp)
	}

	// All three broadcasts emitted: message, session resume, task reopen.
	var sawMessage, sawState, sawTaskReopen bool
	timeout := time.After(3 * time.Second)
	for !sawMessage || !sawState || !sawTaskReopen {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("broadcaster closed unexpectedly")
			}
			switch evt.Type {
			case protocol.EvSessionMessage:
				if sm, ok := evt.Data.(protocol.SessionMessage); ok &&
					sm.SessionID == task.SessionID && sm.Message.Content == followUp {
					sawMessage = true
				}
			case protocol.EvSessionStateChanged:
				if sc, ok := evt.Data.(protocol.SessionStateChanged); ok &&
					sc.SessionID == task.SessionID && sc.Status == protocol.SessionRunning {
					sawState = true
				}
			case protocol.EvTaskStatusChanged:
				if tc, ok := evt.Data.(protocol.TaskStatusChanged); ok &&
					tc.TaskID == task.ID && tc.From == protocol.TaskDone && tc.To == protocol.TaskRunning {
					sawTaskReopen = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for broadcasts: sawMessage=%v sawState=%v sawTaskReopen=%v",
				sawMessage, sawState, sawTaskReopen)
		}
	}

	// The node got session.resume (not session.send_message) with the
	// reattach payload.
	env := waitEnvelope(t, envelopes, protocol.MsgSessionResume)
	var cmd protocol.SessionResumeCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode session.resume: %v", err)
	}
	if cmd.SessionID != task.SessionID || cmd.TaskID != task.ID || cmd.Message != followUp {
		t.Fatalf("resume command = %+v, want session/task/message for %q", cmd, task.SessionID)
	}
	if cmd.ExternalSessionID != extID {
		t.Fatalf("resume external_session_id = %q, want %q", cmd.ExternalSessionID, extID)
	}
	if cmd.AgentType != "fake" || cmd.WorkspacePath == "" {
		t.Fatalf("resume command missing agent/workspace: %+v", cmd)
	}
}

// TestSendMessageResumesFailedSession asserts a failed session revives the
// same way a done one does: message persisted, session running, task reopened,
// session.resume delivered.
func TestSendMessageResumesFailedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("resume-failed-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionFailed, &ended); err != nil {
		t.Fatalf("seed session failed: %v", err)
	}
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskDone); err != nil {
		t.Fatalf("seed task done: %v", err)
	}

	const followUp = "retry, it was a fluke"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": followUp})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q (err %v), want running", sess.Status, err)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil || got.Status != protocol.TaskRunning {
		t.Fatalf("task status = %q (err %v), want running", got.Status, err)
	}

	env := waitEnvelope(t, envelopes, protocol.MsgSessionResume)
	var cmd protocol.SessionResumeCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode session.resume: %v", err)
	}
	if cmd.SessionID != task.SessionID || cmd.Message != followUp {
		t.Fatalf("resume command = %+v, want session %q", cmd, task.SessionID)
	}
}

// TestSendMessageResumeNodeOffline asserts the resume path degrades the same
// way the reply path does when the node is gone: 202 with a queued warning,
// message durable, session and task already moved.
func TestSendMessageResumeNodeOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("resume-offline-node"))

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	// Drop the connection; the offline cascade fails the running session and
	// completes the task, which is exactly the finished state the resume path
	// accepts.
	_ = c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for h.nodeOnline(nodeID) {
		if time.Now().After(deadline) {
			t.Fatal("node still online after close")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		sess, err := st.Queries().GetSession(ctx, task.SessionID)
		if err == nil && sess.Status == protocol.SessionFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offline cascade did not fail the session in time")
		}
		time.Sleep(50 * time.Millisecond)
	}

	const followUp = "resume while offline"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": followUp})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d, want 202; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		Status  string `json:"status"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode 202 body: %v (%s)", err, raw)
	}
	if body.Status != "queued" {
		t.Fatalf("202 body = %+v, want status=queued", body)
	}

	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q (err %v), want running", sess.Status, err)
	}
	got, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil || got.Status != protocol.TaskRunning {
		t.Fatalf("task status = %q (err %v), want running", got.Status, err)
	}
}

// TestNodeResumeRoundTrip wires a scripted node that honors session.resume the
// way the real node daemon does (reattach + prompt) and asserts the follow-up
// reply lands in the session's text buffer — the end-to-end proof that a
// resumed session runs again (session content itself is never persisted
// server-side).
func TestNodeResumeRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("resume-rt-node"))
	defer func() { _ = c.Close() }()

	// Scripted node: on session.resume, answer the follow-up and report done,
	// mirroring what node.resumeSession + the fake adapter produce.
	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgSessionResume {
				continue
			}
			var cmd protocol.SessionResumeCommand
			if err := env.Decode(&cmd); err != nil {
				continue
			}
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionEvent, protocol.SessionMessage{
				SessionID: cmd.SessionID,
				Message: protocol.Message{
					ID:        "resume-reply-1",
					SessionID: cmd.SessionID,
					Role:      protocol.RoleAssistant,
					Content:   "resumed answer to: " + cmd.Message,
					CreatedAt: time.Now().UTC(),
				},
			}))
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
				Type: protocol.EvSessionStateChanged,
				Data: protocol.SessionStateChanged{SessionID: cmd.SessionID, Status: protocol.SessionDone},
				Ts:   time.Now().UTC(),
			}))
		}
	}()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("seed session done: %v", err)
	}
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskDone); err != nil {
		t.Fatalf("seed task done: %v", err)
	}

	const followUp = "one more thing"
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/send", map[string]string{"message": followUp})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, m := range h.textBuffer(ctx, task.SessionID).Messages {
			if m.Role == protocol.RoleAssistant && m.Content == "resumed answer to: "+followUp {
				return // the resumed session ran and answered
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed session's reply never landed in the session text buffer")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestCompletionGateParksTaskInAwaitingUser asserts the gate: a session that
// ends done while mirrored subtasks are still open parks the task in
// awaiting_user instead of done (the session itself stays done so /send can
// revive it).
func TestCompletionGateParksTaskInAwaitingUser(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("gate-park-node"))
	defer func() { _ = c.Close() }()

	// Scripted node: mirror an unfinished todo, then report done.
	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgTaskStart {
				continue
			}
			var cmd protocol.TaskStartCommand
			if err := env.Decode(&cmd); err != nil {
				continue
			}
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgTodoUpdate, protocol.TodoUpdate{
				SessionID: cmd.SessionID,
				Todos: []protocol.Todo{
					{ID: "1", Title: "Research", Status: protocol.SubtaskDone},
					{ID: "2", Title: "Implement", Status: protocol.SubtaskInProgress},
				},
			}))
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
				Type: protocol.EvSessionStateChanged,
				Data: protocol.SessionStateChanged{SessionID: cmd.SessionID, Status: protocol.SessionDone},
				Ts:   time.Now().UTC(),
			}))
		}
	}()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil && got.Status == protocol.TaskAwaitingUser {
			break
		}
		if time.Now().After(deadline) {
			status := ""
			if err == nil {
				status = string(got.Status)
			}
			t.Fatalf("task did not park in awaiting_user; last status=%q", status)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The session itself is done: the gate only holds the task.
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionDone {
		t.Fatalf("session status = %q (err %v), want done", sess.Status, err)
	}
}

// TestCompletionGatePassesWhenSubtasksDone asserts the other branch: a done
// session with zero open subtasks (all done, or none at all) completes the
// task as before.
func TestCompletionGatePassesWhenSubtasksDone(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("gate-pass-node"))
	defer func() { _ = c.Close() }()

	// Scripted node: mirror an all-done todo list, then report done. A second
	// task gets no todos at all (zero-subtask branch).
	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgTaskStart {
				continue
			}
			var cmd protocol.TaskStartCommand
			if err := env.Decode(&cmd); err != nil {
				continue
			}
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgTodoUpdate, protocol.TodoUpdate{
				SessionID: cmd.SessionID,
				Todos: []protocol.Todo{
					{ID: "1", Title: "Research", Status: protocol.SubtaskDone},
					{ID: "2", Title: "Implement", Status: protocol.SubtaskDone},
				},
			}))
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
				Type: protocol.EvSessionStateChanged,
				Data: protocol.SessionStateChanged{SessionID: cmd.SessionID, Status: protocol.SessionDone},
				Ts:   time.Now().UTC(),
			}))
		}
	}()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil && got.Status == protocol.TaskDone {
			return // all-done subtasks: completes as before
		}
		if time.Now().After(deadline) {
			status := ""
			if err == nil {
				status = string(got.Status)
			}
			t.Fatalf("task with all-done subtasks did not complete; last status=%q", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCompletionGateZeroSubtasksCompletes asserts the zero-subtask branch: a
// done session with no mirrored todos at all still completes the task.
func TestCompletionGateZeroSubtasksCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("gate-zero-node"))
	defer func() { _ = c.Close() }()

	go func() {
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != protocol.MsgTaskStart {
				continue
			}
			var cmd protocol.TaskStartCommand
			if err := env.Decode(&cmd); err != nil {
				continue
			}
			// No todo update at all: report done immediately.
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
				Type: protocol.EvSessionStateChanged,
				Data: protocol.SessionStateChanged{SessionID: cmd.SessionID, Status: protocol.SessionDone},
				Ts:   time.Now().UTC(),
			}))
		}
	}()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := st.Queries().GetTask(ctx, task.ID)
		if err == nil && got.Status == protocol.TaskDone {
			return // zero subtasks: completes as before
		}
		if time.Now().After(deadline) {
			status := ""
			if err == nil {
				status = string(got.Status)
			}
			t.Fatalf("task with zero subtasks did not complete; last status=%q", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCreateBacklogTaskStaysWaiting asserts auto_start=false skips dispatch:
// the task is created waiting with no session, and no task.start reaches the
// node.
func TestCreateBacklogTaskStaysWaiting(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("backlog-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	workspace := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID,
		"name":    uniqueName("backlog-ws"),
		"path":    "/tmp/backlog-ws",
	})
	task := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]any{
		"workspace_id": workspace.ID,
		"title":        uniqueName("backlog-task"),
		"prompt":       "do it later",
		"agent_type":   "fake",
		"auto_start":   false,
	})
	if task.Status != protocol.TaskWaiting {
		t.Fatalf("backlog task status = %q, want waiting", task.Status)
	}
	if task.SessionID != "" {
		t.Fatalf("backlog task has session %q, want none", task.SessionID)
	}
	if task.AutoStart {
		t.Fatalf("backlog task auto_start = true, want false")
	}

	// No task.start within a beat: the node must hear nothing.
	select {
	case env := <-envelopes:
		t.Fatalf("node received unexpected envelope %q for a backlog task", env.Type)
	case <-time.After(500 * time.Millisecond):
	}

	// Default creation still dispatches.
	dispatched := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]any{
		"workspace_id": workspace.ID,
		"title":        uniqueName("eager-task"),
		"prompt":       "do it now",
		"agent_type":   "fake",
	})
	if dispatched.Status != protocol.TaskRunning || dispatched.SessionID == "" {
		t.Fatalf("default task not dispatched: %+v", dispatched)
	}
	if !dispatched.AutoStart {
		t.Fatalf("default task auto_start = false, want true")
	}
	// Drain the eager task's start frame so later tests on this node (none —
	// each test dials its own node) stay clean.
	env := waitEnvelope(t, envelopes, protocol.MsgTaskStart)
	var cmd protocol.TaskStartCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode task.start: %v", err)
	}
	if cmd.TaskID != dispatched.ID {
		t.Fatalf("task.start for %q, want %q", cmd.TaskID, dispatched.ID)
	}
}

// TestStartBacklogTask asserts POST /api/tasks/{id}/start dispatches a waiting
// backlog task: session created, forward pointer set, task running, and a
// task.start envelope on the wire.
func TestStartBacklogTask(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("start-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	workspace := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": nodeID,
		"name":    uniqueName("start-ws"),
		"path":    "/tmp/start-ws",
	})
	backlog := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]any{
		"workspace_id": workspace.ID,
		"title":        uniqueName("start-task"),
		"prompt":       "start me",
		"agent_type":   "fake",
		"auto_start":   false,
	})

	events, unsubscribe := h.Bus().Subscribe(EventScope{All: true})
	defer unsubscribe()

	resp, raw := postRaw(t, auth.token, ts.URL+"/api/tasks/"+backlog.ID+"/start", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	started := decodeOK[protocol.Task](t, "start task", resp, raw)
	if started.Status != protocol.TaskRunning || started.SessionID == "" {
		t.Fatalf("started task = %+v, want running with a session", started)
	}

	// The node got task.start for the backlog task.
	env := waitEnvelope(t, envelopes, protocol.MsgTaskStart)
	var cmd protocol.TaskStartCommand
	if err := env.Decode(&cmd); err != nil {
		t.Fatalf("decode task.start: %v", err)
	}
	if cmd.TaskID != backlog.ID || cmd.SessionID != started.SessionID {
		t.Fatalf("task.start = %+v, want task %q session %q", cmd, backlog.ID, started.SessionID)
	}

	// The running flip was broadcast.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("broadcaster closed unexpectedly")
			}
			if evt.Type == protocol.EvTaskStatusChanged {
				if tc, ok := evt.Data.(protocol.TaskStatusChanged); ok &&
					tc.TaskID == backlog.ID && tc.To == protocol.TaskRunning {
					return
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for task.status_changed to running")
		}
	}
}

// TestStartTaskConflicts asserts the 409s: an already-started task (409) and a
// task that is not waiting (409).
func TestStartTaskConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("start-conflict-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	// Already started (session pointer set) -> 409.
	resp, raw := postRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID+"/start", map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start started task: status = %d, want 409; body=%s", resp.StatusCode, raw)
	}

	// A finished task with the pointer cleared is not waiting -> 409.
	ctx := context.Background()
	if err := st.Queries().UpdateTaskSessionID(ctx, task.ID, ""); err != nil {
		t.Fatalf("clear session pointer: %v", err)
	}
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskDone); err != nil {
		t.Fatalf("seed task done: %v", err)
	}
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID+"/start", map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start done task: status = %d, want 409; body=%s", resp.StatusCode, raw)
	}

	// Unknown task -> 404.
	resp, raw = postRaw(t, auth.token, ts.URL+"/api/tasks/00000000-0000-0000-0000-000000000000/start", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("start unknown task: status = %d, want 404; body=%s", resp.StatusCode, raw)
	}
}

// TestTaskArchiveFlow asserts archive semantics: in-flight (running) tasks
// cannot be archived — they must be resolved first — while waiting (shelved)
// and done (finished) tasks can, archiving hides the task from every default
// list/search path, ?archived=1 returns only archived rows, and unarchiving
// restores it.
func TestTaskArchiveFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("archive-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	// A running task cannot be archived.
	resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"archived": true})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("archive running task: status = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode 409 body: %v (%s)", err, raw)
	}
	if errBody.Error != "only waiting or finished tasks can be archived" {
		t.Fatalf("409 body = %+v, want only-waiting-or-finished error", errBody)
	}

	// Pausing (running -> waiting) unlocks archiving: shelved work can be put
	// away without finishing it. Unarchive afterwards so the task can be
	// finished below.
	if resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"status": "waiting"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("running -> waiting (pause): status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"archived": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive waiting task: status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"archived": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("unarchive waiting task: status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// Finish the task, then archive.
	if _, err := st.Queries().UpdateTaskStatus(ctx, task.ID, protocol.TaskDone); err != nil {
		t.Fatalf("seed task done: %v", err)
	}
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"archived": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive done task: status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var archived protocol.Task
	if err := json.Unmarshal(raw, &archived); err != nil {
		t.Fatalf("decode archived task: %v (%s)", err, raw)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived task has nil archived_at: %+v", archived)
	}

	wsID := archived.WorkspaceID
	lists := map[string]string{
		"workspace list":  ts.URL + "/api/tasks?workspace_id=" + wsID,
		"node list":       ts.URL + "/api/tasks?node_id=" + nodeID,
		"workspace query": ts.URL + "/api/tasks?workspace_id=" + wsID + "&q=" + task.Title,
		"node query":      ts.URL + "/api/tasks?node_id=" + nodeID + "&q=" + task.Title,
	}
	for name, url := range lists {
		resp, raw := getRaw(t, auth.token, url)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", name, resp.StatusCode, raw)
		}
		var rows []protocol.Task
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("%s: decode: %v (%s)", name, err, raw)
		}
		for _, row := range rows {
			if row.ID == task.ID {
				t.Fatalf("%s leaks archived task %s", name, task.ID)
			}
		}
	}

	// ?archived=1 returns ONLY archived rows: the archived task is present,
	// and a live task is not.
	live := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	archivedLists := map[string]string{
		"workspace list": ts.URL + "/api/tasks?workspace_id=" + wsID + "&archived=1",
		"node list":      ts.URL + "/api/tasks?node_id=" + nodeID + "&archived=1",
	}
	for name, url := range archivedLists {
		resp, raw := getRaw(t, auth.token, url)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", name, resp.StatusCode, raw)
		}
		var rows []protocol.Task
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("%s: decode: %v (%s)", name, err, raw)
		}
		var sawArchived, sawLive bool
		for _, row := range rows {
			switch row.ID {
			case task.ID:
				sawArchived = true
			case live.ID:
				sawLive = true
			}
		}
		if !sawArchived {
			t.Fatalf("%s misses archived task %s (got %d rows)", name, task.ID, len(rows))
		}
		if sawLive {
			t.Fatalf("%s leaks live task %s into the archived-only view", name, live.ID)
		}
	}

	// Unarchive restores the task to the default view.
	resp, raw = patchRaw(t, auth.token, ts.URL+"/api/tasks/"+task.ID, map[string]any{"archived": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unarchive: status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var restored protocol.Task
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("decode restored task: %v (%s)", err, raw)
	}
	if restored.ArchivedAt != nil {
		t.Fatalf("restored task still archived: %+v", restored)
	}
	resp, raw = getRaw(t, auth.token, ts.URL+"/api/tasks?workspace_id="+wsID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default list after unarchive: status = %d", resp.StatusCode)
	}
	var rows []protocol.Task
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode default list: %v (%s)", err, raw)
	}
	var found bool
	for _, row := range rows {
		if row.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unarchived task %s missing from the default list", task.ID)
	}
}

// TestListMessagesReplaysFromNode asserts the history proxy: a finished
// session's GET /messages is answered by replaying the agent-side session
// through the owning node (session content is never stored on the hub).
func TestListMessagesReplaysFromNode(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hist-node"))
	defer func() { _ = c.Close() }()
	envelopes := readEnvelopes(c)

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	const extID = "agent-session-ext-1"
	if err := st.Queries().UpdateSessionExternalID(ctx, task.SessionID, extID); err != nil {
		t.Fatalf("seed external id: %v", err)
	}
	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("seed session done: %v", err)
	}

	// Scripted node: answer the history request with a replayed conversation,
	// reporting the request it received for the assertions below.
	reqCh := make(chan protocol.SessionHistoryRequest, 1)
	go func() {
		for env := range envelopes {
			if env.Type != protocol.MsgSessionHistory {
				continue
			}
			var req protocol.SessionHistoryRequest
			if err := env.Decode(&req); err != nil {
				continue
			}
			select {
			case reqCh <- req:
			default:
			}
			_ = c.WriteJSON(protocol.NewEnvelope(protocol.MsgSessionHistoryResp, protocol.SessionHistoryResponse{
				ReqID:     req.ReqID,
				SessionID: req.SessionID,
				Messages: []protocol.Message{
					{ID: "r1", SessionID: req.SessionID, Role: protocol.RoleUser, Content: "question"},
					{ID: "r2", SessionID: req.SessionID, Role: protocol.RoleAssistant, Content: "answer"},
				},
			}))
		}
	}()

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/messages")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var msgs []protocol.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("decode messages: %v (%s)", err, raw)
	}
	if len(msgs) != 2 || msgs[0].Content != "question" || msgs[1].Content != "answer" {
		t.Fatalf("replayed messages = %+v, want [question answer]", msgs)
	}

	select {
	case req := <-reqCh:
		if req.SessionID != task.SessionID || req.ExternalSessionID != extID {
			t.Fatalf("history request = %+v, want session %q external %q", req, task.SessionID, extID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no session.history_req reached the node")
	}
}

// TestListMessagesFinishedUnavailable asserts the privacy trade-off: a
// finished session whose node cannot replay yields 503 — history lives only
// with the agent, and the hub reports that instead of inventing a transcript.
func TestListMessagesFinishedUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	h, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hist-off-node"))
	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}
	ctx := context.Background()

	const extID = "agent-session-ext-gone"
	if err := st.Queries().UpdateSessionExternalID(ctx, task.SessionID, extID); err != nil {
		t.Fatalf("seed external id: %v", err)
	}
	ended := time.Now().UTC()
	if err := st.Queries().UpdateSessionStatus(ctx, task.SessionID, protocol.SessionDone, &ended); err != nil {
		t.Fatalf("seed session done: %v", err)
	}

	// Drop the node and wait until the hub reflects it, so the read path
	// deterministically hits "offline" rather than racing the disconnect.
	_ = c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.nodesMu.Lock()
		_, online := h.nodes[nodeID]
		h.nodesMu.Unlock()
		if !online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("node never went offline")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The prompt is real content the hub still holds, so it is served even
	// though the agent-side session is gone.
	resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/messages")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var msgs []protocol.Message
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %s (err %v), want the buffered prompt", raw, err)
	}

	// A hub restart drops the buffer; with no replay path left, the endpoint
	// reports that history is unavailable.
	h.updateTextBuffer(ctx, task.SessionID, func(st *sessionText) { st.Messages = nil })
	resp, raw = getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/messages")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("messages status = %d, want 503; body=%s", resp.StatusCode, raw)
	}
}

// TestUserInputPromptStaysOutOfEventStream pins the privacy invariant: the
// ask's prompt is content — it reaches live clients and the ephemeral buffer
// (the session snapshot's pending_input) but is never persisted; the stored
// event carries only its occurrence.
func TestUserInputPromptStaysOutOfEventStream(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("ask-node"))
	defer func() { _ = c.Close() }()

	task := createWorkspaceAndTask(t, auth.token, ts, nodeID)
	if task.SessionID == "" {
		t.Fatalf("task has no session: %+v", task)
	}

	const prompt = "may I run rm -rf /tmp/x?"
	if err := c.WriteJSON(protocol.NewEnvelope(protocol.MsgEvent, protocol.Event{
		Type: protocol.EvSessionUserInputRequest,
		Data: protocol.SessionUserInputRequested{SessionID: task.SessionID, Prompt: prompt},
		Ts:   time.Now().UTC(),
	})); err != nil {
		t.Fatalf("send user_input_requested: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, raw := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("snapshot status = %d; body=%s", resp.StatusCode, raw)
		}
		var snap struct {
			Session      protocol.Session `json:"session"`
			PendingInput string           `json:"pending_input"`
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatalf("decode snapshot: %v (%s)", err, raw)
		}
		if snap.PendingInput == prompt {
			// The persisted event stream must not contain the prompt anywhere.
			resp2, raw2 := getRaw(t, auth.token, ts.URL+"/api/sessions/"+task.SessionID+"/events")
			if resp2.StatusCode != http.StatusOK {
				t.Fatalf("events status = %d; body=%s", resp2.StatusCode, raw2)
			}
			if bytes.Contains(raw2, []byte("may I run")) {
				t.Fatalf("prompt leaked into the persisted event stream: %s", raw2)
			}
			if snap.Session.Status != protocol.SessionAwaitingUser {
				t.Fatalf("session status = %q, want awaiting_user", snap.Session.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending input never reached the session snapshot (got %q)", snap.PendingInput)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The title is only the board label: the API derives it from the prompt when a
// caller omits it.
func TestTaskTitleFromPrompt(t *testing.T) {
	cases := []struct{ prompt, want string }{
		{"update flake inputs", "update flake inputs"},
		{"\n\n  first line\nsecond line", "first line"},
		{strings.Repeat("あ", 100), strings.Repeat("あ", 80) + "…"},
		{"   \n\t\n", ""},
	}
	for _, c := range cases {
		if got := taskTitleFromPrompt(c.prompt); got != c.want {
			t.Fatalf("taskTitleFromPrompt(%q) = %q, want %q", c.prompt, got, c.want)
		}
	}
}
