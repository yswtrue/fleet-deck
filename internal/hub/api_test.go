package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
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

	events, unsubscribe := h.Bus().Subscribe()
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
		t.Fatalf("returned message = %+v, want a persisted user message %q", body.Message, reply)
	}

	// Session resumed.
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q, want running", sess.Status)
	}

	// Message persisted.
	msgs, err := st.Queries().ListMessagesBySession(ctx, task.SessionID, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var found bool
	for _, m := range msgs {
		if m.Role == protocol.RoleUser && m.Content == reply {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message %q not persisted; got %d rows", reply, len(msgs))
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

	_, st, ts, auth := newTestHub(t)
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
	if body.Error != "session is not awaiting user input" || body.Status != string(protocol.SessionRunning) {
		t.Fatalf("409 body = %+v, want status=running", body)
	}

	ctx := context.Background()
	sess, err := st.Queries().GetSession(ctx, task.SessionID)
	if err != nil || sess.Status != protocol.SessionRunning {
		t.Fatalf("session status = %q (err %v), want running", sess.Status, err)
	}
	msgs, err := st.Queries().ListMessagesBySession(ctx, task.SessionID, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, m := range msgs {
		if m.Role == protocol.RoleUser {
			t.Fatalf("unexpected user message persisted: %+v", m)
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

	// The message is still durable and the session resumed.
	msgs, err := st.Queries().ListMessagesBySession(ctx, task.SessionID, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var found bool
	for _, m := range msgs {
		if m.Role == protocol.RoleUser && m.Content == reply {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message %q not persisted while node offline", reply)
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

	events, unsubscribe := h.Bus().Subscribe()
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

	// The body must carry exactly the two SessionSnapshot fields.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode raw keys: %v (%s)", err, raw)
	}
	if len(keys) != 2 || keys["session"] == nil || keys["task"] == nil {
		t.Fatalf("response keys = %v, want exactly session+task", keys)
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
	defer resp.Body.Close()
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

// TestCreateNodeWithoutCapabilities pins the optional-capabilities contract: a
// missing capabilities field stores as an empty list, not a 500.
func TestCreateNodeWithoutCapabilities(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)

	node := postJSON[protocol.Node](t, auth.token, ts.URL+"/api/nodes", map[string]string{
		"name": uniqueName("it-node"),
	})
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM node WHERE id = $1", node.ID); err != nil {
			t.Logf("cleanup: delete node: %v", err)
		}
	})
	if len(node.Capabilities) != 0 {
		t.Fatalf("capabilities = %#v, want empty list (omitted on the wire)", node.Capabilities)
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
	if len(ids) != 2 || !(ids[0] == report.ID || ids[1] == report.ID) || !(ids[0] == retry.ID || ids[1] == retry.ID) {
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

// TestPatchTaskTransitionValidation pins the manual transition table: done is
// terminal, same-status is idempotent, illegal moves are 409.
func TestPatchTaskTransitionValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}

	_, st, ts, auth := newTestHub(t)
	c, nodeID := dialNode(t, ts, st, uniqueName("hub-it-patch"))
	defer func() { _ = c.Close() }()

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
	offline := postJSON[protocol.Node](t, auth.token, ts.URL+"/api/nodes", map[string]string{
		"name": uniqueName("it-offline"),
	})
	offWS := postJSON[protocol.Workspace](t, auth.token, ts.URL+"/api/workspaces", map[string]string{
		"node_id": offline.ID, "name": uniqueName("it-ws"), "path": "/tmp/it-ws",
	})
	task2 := postJSON[protocol.Task](t, auth.token, ts.URL+"/api/tasks", map[string]string{
		"workspace_id": offWS.ID, "title": uniqueName("it-task"), "prompt": "p", "agent_type": "fake",
	})
	if resp, raw := patchStatus(task2.ID, "awaiting_user"); resp.StatusCode != http.StatusOK {
		t.Fatalf("waiting -> awaiting_user status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := patchStatus(task2.ID, "running"); resp.StatusCode != http.StatusOK {
		t.Fatalf("awaiting_user -> running status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
}

// TestGetNodeByID covers GET /api/nodes/{id}, the lookup behind the web UI's
// /nodes/:nodeId deep links: any authenticated user may read it, unknown ids
// are 404, and anonymous callers are 401.
func TestGetNodeByID(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, st, ts, auth := newTestHub(t)
	_, userToken := setupUserWithRole(t, st, "user")

	node := postJSON[protocol.Node](t, auth.token, ts.URL+"/api/nodes", map[string]string{
		"name": uniqueName("it-node-get"),
	})

	resp, raw := getRaw(t, auth.token, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node (admin)", resp, raw, http.StatusOK)
	if got := decodeOK[protocol.Node](t, "get node", resp, raw); got.Name != node.Name {
		t.Fatalf("get node name = %q, want %q", got.Name, node.Name)
	}

	resp, raw = getRaw(t, userToken, ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node (user)", resp, raw, http.StatusOK)

	resp, raw = getRaw(t, auth.token, ts.URL+"/api/nodes/00000000-0000-0000-0000-000000000000")
	requireStatus(t, "get unknown node", resp, raw, http.StatusNotFound)

	resp, raw = getRaw(t, "", ts.URL+"/api/nodes/"+node.ID)
	requireStatus(t, "get node without token", resp, raw, http.StatusUnauthorized)
}
