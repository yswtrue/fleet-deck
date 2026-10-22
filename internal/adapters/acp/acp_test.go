package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// ---- recording sink --------------------------------------------------------

type recEvent struct {
	kind   string
	text   string
	todos  []protocol.Todo
	status protocol.SessionStatus
	prompt string
	tokens adapters.Usage
}

type recorder struct {
	mu     sync.Mutex
	events []recEvent

	done      chan struct{}
	doneOne   sync.Once
	errCh     chan string
	textCh    chan string
	todoCh    chan []protocol.Todo
	thoughtCh chan string
	userCh    chan string
	stateCh   chan protocol.SessionStatus
}

func newRecorder() *recorder {
	return &recorder{
		done:      make(chan struct{}),
		errCh:     make(chan string, 4),
		textCh:    make(chan string, 64),
		todoCh:    make(chan []protocol.Todo, 8),
		thoughtCh: make(chan string, 16),
		userCh:    make(chan string, 4),
		stateCh:   make(chan protocol.SessionStatus, 64),
	}
}

func (r *recorder) record(e recEvent) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) TextDelta(_ context.Context, _ string, text string) {
	r.record(recEvent{kind: "text_delta", text: text})
	select {
	case r.textCh <- text:
	default:
	}
}

func (r *recorder) Message(_ context.Context, _ string, _ protocol.Message) {
	r.record(recEvent{kind: "message"})
}

func (r *recorder) TodoUpdate(_ context.Context, _ string, todos []protocol.Todo) {
	cp := make([]protocol.Todo, len(todos))
	copy(cp, todos)
	r.record(recEvent{kind: "todo_update", todos: cp})
	select {
	case r.todoCh <- cp:
	default:
	}
}

func (r *recorder) StateChange(_ context.Context, _ string, status protocol.SessionStatus) {
	r.record(recEvent{kind: "state_change", status: status})
	select {
	case r.stateCh <- status:
	default:
	}
}

func (r *recorder) UserInputRequested(_ context.Context, _ string, prompt string) {
	r.record(recEvent{kind: "user_input", prompt: prompt})
	select {
	case r.userCh <- prompt:
	default:
	}
}

func (r *recorder) Usage(_ context.Context, _ string, u adapters.Usage) {
	r.record(recEvent{kind: "usage", text: u.Model, tokens: u})
}

func (r *recorder) Done(_ context.Context, _ string) {
	r.record(recEvent{kind: "done"})
	r.doneOne.Do(func() { close(r.done) })
}

func (r *recorder) Error(_ context.Context, _ string, err error) {
	r.record(recEvent{kind: "error", text: err.Error()})
	select {
	case r.errCh <- err.Error():
	default:
	}
}

func (r *recorder) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Done")
	}
}

func (r *recorder) waitError(t *testing.T) string {
	t.Helper()
	select {
	case e := <-r.errCh:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Error")
		return ""
	}
}

func (r *recorder) waitUserInput(t *testing.T) string {
	t.Helper()
	select {
	case p := <-r.userCh:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for UserInputRequested")
		return ""
	}
}

func (r *recorder) waitTodos(t *testing.T) []protocol.Todo {
	t.Helper()
	select {
	case todos := <-r.todoCh:
		return todos
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for TodoUpdate")
		return nil
	}
}

func (r *recorder) waitState(t *testing.T, want protocol.SessionStatus) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-r.stateCh:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %q", want)
		}
	}
}

func (r *recorder) waitTextContaining(t *testing.T, substr string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case text := <-r.textCh:
			if strings.Contains(text, substr) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for text containing %q", substr)
		}
	}
}

// ThoughtDelta records the agent's reasoning stream (ThoughtSink).
func (r *recorder) ThoughtDelta(_ context.Context, _ string, text string) {
	r.record(recEvent{kind: "thought", text: text})
	select {
	case r.thoughtCh <- text:
	default:
	}
}

func (r *recorder) thoughts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if e.kind == "thought" {
			out = append(out, e.text)
		}
	}
	return out
}

func (r *recorder) texts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if e.kind == "text_delta" {
			out = append(out, e.text)
		}
	}
	return out
}

// usages returns the recorded usage reports in order.
func (r *recorder) usages() []adapters.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []adapters.Usage
	for _, e := range r.events {
		if e.kind == "usage" {
			out = append(out, e.tokens)
		}
	}
	return out
}

// ---- test harness ----------------------------------------------------------

type adapterOpts struct {
	scenario  string
	resume    string
	grace     time.Duration
	workspace string // defaults to a fresh temp dir
}

// testBinary returns the absolute path to this test binary, which the fake
// agent subprocess re-executes.
func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// startTestAdapter launches the adapter against the in-binary fake ACP agent.
func startTestAdapter(t *testing.T, opts adapterOpts) (*Adapter, *recorder) {
	t.Helper()
	a := New("test")
	a.cfg = agentConfig{
		command: testBinary(t),
		args:    []string{"-test.run=TestFakeACPAgent"},
		env: []string{
			"FLEETDECK_FAKE_ACP=1",
			"FLEETDECK_FAKE_ACP_SCENARIO=" + opts.scenario,
		},
	}
	a.ResumeSessionID = opts.resume
	a.CancelGrace = opts.grace

	rec := newRecorder()
	workspace := opts.workspace
	if workspace == "" {
		workspace = t.TempDir()
	}
	if _, err := a.Start(context.Background(), adapters.StartRequest{
		SessionID:     "sess-1",
		TaskID:        "task-1",
		AgentType:     "test",
		Prompt:        "do the thing",
		WorkspacePath: workspace,
	}, rec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Cancel(context.Background(), "sess-1") })
	return a, rec
}

// ---- tests -----------------------------------------------------------------

func TestTextDeltaMapping(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "deltas"})
	rec.waitDone(t)

	texts := rec.texts()
	if len(texts) != 3 {
		t.Fatalf("TextDelta calls = %d, want 3: %v", len(texts), texts)
	}
	if got := strings.Join(texts, ""); got != "Hello, world" {
		t.Fatalf("concatenated text = %q, want %q", got, "Hello, world")
	}
}

// Reasoning travels on its own stream, not as assistant text.
func TestThoughtDeltaMapping(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "deltas"})
	rec.waitDone(t)

	thoughts := rec.thoughts()
	if len(thoughts) != 1 || thoughts[0] != "planning the answer" {
		t.Fatalf("thoughts = %v, want the reasoning chunk", thoughts)
	}
	if got := strings.Join(rec.texts(), ""); got != "Hello, world" {
		t.Fatalf("assistant text = %q, want reasoning excluded", got)
	}
}

// TestUsageCaptureFromPromptResult pins the usage mapping on the shape a real
// opencode prompt result carries (see the package comment): inputTokens ->
// PromptTokens, outputTokens -> CompletionTokens, totalTokens -> TotalTokens,
// verbatim; thoughtTokens stay inside the agent's total.
func TestUsageCaptureFromPromptResult(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "usage"})
	rec.waitDone(t)

	usages := rec.usages()
	if len(usages) != 1 {
		t.Fatalf("Usage calls = %d, want 1: %+v", len(usages), usages)
	}
	want := adapters.Usage{Model: "fake/model-x", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 17}
	if usages[0] != want {
		t.Fatalf("usage = %+v, want %+v", usages[0], want)
	}
}

// TestUsageModelFallsBackToAgentType pins the documented model fallback: an
// agent that reports no model config option records the agent_type instead.
func TestUsageModelFallsBackToAgentType(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "usage-nomodel"})
	rec.waitDone(t)

	usages := rec.usages()
	if len(usages) != 1 {
		t.Fatalf("Usage calls = %d, want 1: %+v", len(usages), usages)
	}
	if usages[0].Model != "test" {
		t.Fatalf("model = %q, want the agent_type fallback %q", usages[0].Model, "test")
	}
}

// TestNoUsageWhenAgentReportsNone pins the never-fabricate rule: a prompt
// result without a usage field records nothing.
func TestNoUsageWhenAgentReportsNone(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "deltas"})
	rec.waitDone(t)

	if usages := rec.usages(); len(usages) != 0 {
		t.Fatalf("Usage calls = %d, want 0 (agent reported no usage): %+v", len(usages), usages)
	}
}

func TestTodoMirror(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "todo"})
	todos := rec.waitTodos(t)

	if len(todos) != 3 {
		t.Fatalf("todo count = %d, want 3: %+v", len(todos), todos)
	}
	want := []struct {
		title  string
		status protocol.SubtaskStatus
	}{
		{"Research", protocol.SubtaskDone},
		{"Implement", protocol.SubtaskInProgress},
		{"Review", protocol.SubtaskPending},
	}
	for i, w := range want {
		if todos[i].Title != w.title || todos[i].Status != w.status {
			t.Errorf("todo[%d] = {%q,%q}, want {%q,%q}",
				i, todos[i].Title, todos[i].Status, w.title, w.status)
		}
	}
	rec.waitDone(t)
}

func TestPermissionFlow(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "permission"})
	prompt := rec.waitUserInput(t)

	if !strings.Contains(prompt, "[permission_request] options=") {
		t.Fatalf("prompt missing permission marker: %q", prompt)
	}
	if !strings.Contains(prompt, `"id":"allow"`) || !strings.Contains(prompt, `"label":"Allow"`) {
		t.Fatalf("prompt options not serialized as {id,label}: %q", prompt)
	}
	if !strings.Contains(prompt, "Run shell command") {
		t.Fatalf("prompt missing agent question: %q", prompt)
	}
	// The node re-emits this after a reconnect; it must survive until answered.
	if pending, ok := a.PendingAsk(); !ok || pending != prompt {
		t.Fatalf("PendingAsk = %q,%v, want the outstanding prompt", pending, ok)
	}

	if err := a.SendMessage(context.Background(), "sess-1", `[permission_response] option_id="allow"`); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The fake agent echoes the selected option back, proving the adapter
	// forwarded it as the ACP permission response.
	rec.waitTextContaining(t, "perm:allow")
	rec.waitDone(t)
	if pending, ok := a.PendingAsk(); ok {
		t.Fatalf("PendingAsk = %q after the reply, want none", pending)
	}
}

func TestPermissionReplyUnparseableDefaultsToDeny(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "permission"})
	rec.waitUserInput(t)

	if err := a.SendMessage(context.Background(), "sess-1", "sure, go ahead"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// The reject option is the fallback, never the allow option.
	rec.waitTextContaining(t, "perm:deny")
	rec.waitDone(t)
}

func TestPermissionReplyUnknownOptionIDDefaultsToDeny(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "permission"})
	rec.waitUserInput(t)

	// A well-formed marker naming an option the agent never offered must not
	// reach the agent.
	if err := a.SendMessage(context.Background(), "sess-1", `[permission_response] option_id="evil"`); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	rec.waitTextContaining(t, "perm:deny")
	rec.waitDone(t)
}

func TestPermissionReplyEmptyOptionIDDefaultsToDeny(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "permission"})
	rec.waitUserInput(t)

	if err := a.SendMessage(context.Background(), "sess-1", `[permission_response] option_id=""`); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	rec.waitTextContaining(t, "perm:deny")
	rec.waitDone(t)
}

func TestInitializeAdvertisesClientCapabilities(t *testing.T) {
	raw, err := json.Marshal(initializeParams{
		ProtocolVersion: 1,
		ClientCapabilities: clientCapabilities{
			FS:          fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			Elicitation: elicitationCapabilities{},
			Terminal:    true,
		},
	})
	if err != nil {
		t.Fatalf("marshal initialize: %v", err)
	}
	var got struct {
		ClientCapabilities map[string]json.RawMessage `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}
	if string(got.ClientCapabilities["elicitation"]) != `{"form":{}}` {
		t.Fatalf("elicitation capability = %s, want {\"form\":{}}",
			got.ClientCapabilities["elicitation"])
	}
	if string(got.ClientCapabilities["terminal"]) != "true" {
		t.Fatalf("terminal capability = %s, want true", got.ClientCapabilities["terminal"])
	}
}

func TestElicitationAccept(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "elicitation"})
	prompt := rec.waitUserInput(t)

	if !strings.Contains(prompt, "How should I proceed?") {
		t.Fatalf("prompt missing agent message: %q", prompt)
	}
	if !strings.Contains(prompt, "[elicitation_request] schema=") {
		t.Fatalf("prompt missing elicitation marker: %q", prompt)
	}
	if !strings.Contains(prompt, `"required":["value"]`) ||
		!strings.Contains(prompt, `"enum":["allow","deny"]`) {
		t.Fatalf("prompt missing schema: %q", prompt)
	}

	if err := a.SendMessage(context.Background(), "sess-1",
		`[elicitation_response] action="accept" content={"value":"allow"}`); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The fake agent echoes the action and content, proving the adapter
	// forwarded an accept with the user's value.
	rec.waitTextContaining(t, `elicit:accept:{"value":"allow"}`)
	rec.waitDone(t)
}

func TestElicitationDecline(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "elicitation"})
	rec.waitUserInput(t)

	if err := a.SendMessage(context.Background(), "sess-1",
		`[elicitation_response] action="decline"`); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	rec.waitTextContaining(t, "elicit:decline")
	rec.waitDone(t)
}

func TestElicitationInvalidContentCancels(t *testing.T) {
	tests := map[string]string{
		"unknown enum value": `[elicitation_response] action="accept" content={"value":"evil"}`,
		"missing required":   `[elicitation_response] action="accept" content={}`,
		"unparseable reply":  `sure, go ahead`,
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			a, rec := startTestAdapter(t, adapterOpts{scenario: "elicitation"})
			rec.waitUserInput(t)

			if err := a.SendMessage(context.Background(), "sess-1", reply); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			// Invalid or absent content must never be forwarded as consent.
			rec.waitTextContaining(t, "elicit:cancel")
			rec.waitDone(t)
		})
	}
}

func TestElicitationRejectsUnadvertisedURLMode(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "elicitation-url"})

	rec.waitTextContaining(t, "elicit-error:")
	rec.waitDone(t)
	for _, e := range rec.events {
		if e.kind == "user_input" {
			t.Fatal("URL-mode elicitation must be refused before asking the user")
		}
	}
}

func TestTerminalFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake terminal scenarios use sh/sleep")
	}
	_, rec := startTestAdapter(t, adapterOpts{scenario: "terminal"})
	rec.waitTextContaining(t, "terminal:hello-from-terminal:exit=true:released=true:invalid-after=true")
	rec.waitDone(t)
}

func TestTerminalKillAndRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake terminal scenarios use sh/sleep")
	}
	_, rec := startTestAdapter(t, adapterOpts{scenario: "terminal-kill"})
	rec.waitTextContaining(t, "terminal-kill:killed=true:signal=killed:released=true")
	rec.waitDone(t)
}

func TestBoundedOutputTruncatesFromStart(t *testing.T) {
	b := newBoundedOutput(5)
	if _, err := b.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	output, truncated := b.snapshot()
	if output != "bcdef" || !truncated {
		t.Fatalf("snapshot = %q truncated=%v, want %q true", output, truncated, "bcdef")
	}
}

func TestBoundedOutputDropsPartialLeadingRune(t *testing.T) {
	b := newBoundedOutput(2)
	// "€" is three bytes; keeping the last two leaves continuation bytes.
	if _, err := b.Write([]byte("a€")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	output, truncated := b.snapshot()
	if output != "" || !truncated {
		t.Fatalf("snapshot = %q truncated=%v, want empty true", output, truncated)
	}
}

func TestTerminalCreateRejectsCwdOutsideWorkspace(t *testing.T) {
	m := newTerminalManager()
	if _, err := m.create(terminalCreateParams{Command: "true", Cwd: "/"}, t.TempDir()); err == nil {
		t.Fatal("create: expected cwd escape to be rejected")
	}
}

func TestTerminalEnvOverridesAndValidation(t *testing.T) {
	env, err := terminalEnv([]terminalEnvVar{
		{Name: "FLEETDECK_TERM_TEST", Value: "first"},
		{Name: "FLEETDECK_TERM_TEST", Value: "second"},
	})
	if err != nil {
		t.Fatalf("terminalEnv: %v", err)
	}
	count := 0
	for _, entry := range env {
		if entry == "FLEETDECK_TERM_TEST=second" {
			count++
		}
		if entry == "FLEETDECK_TERM_TEST=first" {
			t.Fatal("first value survived an override")
		}
	}
	if count != 1 {
		t.Fatalf("override occurrences = %d, want 1", count)
	}
	if _, err := terminalEnv([]terminalEnvVar{{Name: "BAD=NAME", Value: "x"}}); err == nil {
		t.Fatal("terminalEnv: expected invalid name to be rejected")
	}
}

func TestCancel(t *testing.T) {
	a, rec := startTestAdapter(t, adapterOpts{scenario: "wait", grace: 100 * time.Millisecond})
	rec.waitState(t, protocol.SessionRunning)

	if err := a.Cancel(context.Background(), "sess-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	rec.waitDone(t)
}

func TestAgentCrash(t *testing.T) {
	_, rec := startTestAdapter(t, adapterOpts{scenario: "crash"})
	errText := rec.waitError(t)
	if !strings.Contains(errText, "agent exited") {
		t.Fatalf("error = %q, want it to mention agent exit", errText)
	}
}

func TestSessionResume(t *testing.T) {
	a := New("test")
	a.cfg = agentConfig{
		command: testBinary(t),
		args:    []string{"-test.run=TestFakeACPAgent"},
		env:     []string{"FLEETDECK_FAKE_ACP=1", "FLEETDECK_FAKE_ACP_SCENARIO=default"},
	}
	a.ResumeSessionID = "agent-sess-loaded"

	rec := newRecorder()
	res, err := a.Start(context.Background(), adapters.StartRequest{
		SessionID:     "sess-1",
		Prompt:        "continue",
		WorkspacePath: t.TempDir(),
	}, rec)
	if err != nil {
		t.Fatalf("Start (resume): %v", err)
	}
	t.Cleanup(func() { _ = a.Cancel(context.Background(), "sess-1") })

	if res.ExternalSessionID != "agent-sess-loaded" {
		t.Fatalf("ExternalSessionID = %q, want %q", res.ExternalSessionID, "agent-sess-loaded")
	}
	rec.waitDone(t)
}

func TestBinaryNotFound(t *testing.T) {
	a := New("codex")
	a.cfg = agentConfig{command: "definitely-not-a-real-acp-agent-xyz"}

	_, err := a.Start(context.Background(), adapters.StartRequest{SessionID: "sess-1", Prompt: "hi"}, newRecorder())
	if err == nil {
		t.Fatal("Start: got nil error, want exec.ErrNotFound")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap exec.ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), `binary for "codex" not found`) {
		t.Fatalf("error = %v, want agent-type-specific message", err)
	}
}

// TestDefaultRegistryHasACPAgents asserts registry.go wired all four agent
// types into the process-wide registry.
func TestDefaultRegistryHasACPAgents(t *testing.T) {
	for _, agentType := range []string{"codex", "claude", "opencode", "pi", "omp"} {
		ad, ok := adapters.Default.Get(agentType)
		if !ok {
			t.Fatalf("adapters.Default has no adapter for %q", agentType)
		}
		if _, isACP := ad.(*Adapter); !isACP {
			t.Fatalf("adapters.Default[%q] = %T, want *acp.Adapter", agentType, ad)
		}
	}
}

// ---- pure parser tests -----------------------------------------------------

func TestParseTodoToolCall(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		raw     string
		wantLen int
		want    []protocol.Todo
	}{
		{
			name:    "claude todo_write object",
			title:   "TodoWrite",
			raw:     `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"},{"content":"c","status":"pending"}]}`,
			wantLen: 3,
			want: []protocol.Todo{
				{ID: "1", Title: "a", Status: protocol.SubtaskDone},
				{ID: "2", Title: "b", Status: protocol.SubtaskInProgress},
				{ID: "3", Title: "c", Status: protocol.SubtaskPending},
			},
		},
		{
			name:    "bare array with title field",
			title:   "update_plan",
			raw:     `[{"id":"x","title":"one","status":"done"}]`,
			wantLen: 1,
			want:    []protocol.Todo{{ID: "x", Title: "one", Status: protocol.SubtaskDone}},
		},
		{
			name:    "non-todo title ignored",
			title:   "read_file",
			raw:     `{"todos":[{"content":"a","status":"done"}]}`,
			wantLen: 0,
		},
		{
			name:    "malformed payload skipped",
			title:   "todo_write",
			raw:     `{"todos":"not-an-array"}`,
			wantLen: 0,
		},
		{
			name:    "empty rawInput skipped",
			title:   "todo_write",
			raw:     "",
			wantLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTodoToolCall(tc.title, json.RawMessage(tc.raw))
			if len(got) != tc.wantLen {
				t.Fatalf("got %d todos (%+v), want %d", len(got), got, tc.wantLen)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("todo[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParsePlan(t *testing.T) {
	got := parsePlan([]planEntry{
		{Content: "first", Status: "completed"},
		{Content: "second", Status: "in_progress"},
	})
	if len(got) != 2 {
		t.Fatalf("parsePlan len = %d, want 2", len(got))
	}
	if got[0].Status != protocol.SubtaskDone || got[1].Status != protocol.SubtaskInProgress {
		t.Fatalf("parsePlan statuses = %+v", got)
	}
}

func TestDenyOptionID(t *testing.T) {
	opts := []permissionOptionRaw{
		{OptionID: "a", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "d", Name: "Reject", Kind: "reject_once"},
	}
	if got := denyOptionID(opts); got != "d" {
		t.Fatalf("denyOptionID = %q, want %q", got, "d")
	}

	// No explicit reject kind: fall back to a label mentioning deny.
	byLabel := []permissionOptionRaw{
		{OptionID: "a", Name: "Allow", Kind: "allow_once"},
		{OptionID: "n", Name: "Deny always", Kind: "allow_always"},
	}
	if got := denyOptionID(byLabel); got != "n" {
		t.Fatalf("denyOptionID(label) = %q, want %q", got, "n")
	}

	if got := denyOptionID(nil); got != "" {
		t.Fatalf("denyOptionID(nil) = %q, want empty", got)
	}

	// No reject-ish option: return "" so the caller cancels — an arbitrary
	// last option could be an allow one, and permission is never auto-approved.
	allowOnly := []permissionOptionRaw{
		{OptionID: "a1", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "a2", Name: "Allow always", Kind: "allow_always"},
	}
	if got := denyOptionID(allowOnly); got != "" {
		t.Fatalf("denyOptionID(allow-only) = %q, want empty", got)
	}

	// Options with an empty id can never be selected.
	emptyIDs := []permissionOptionRaw{
		{OptionID: "", Name: "Reject", Kind: "reject_once"},
		{OptionID: "", Name: "Deny always", Kind: ""},
	}
	if got := denyOptionID(emptyIDs); got != "" {
		t.Fatalf("denyOptionID(empty ids) = %q, want empty", got)
	}
}

func TestKnownOptionID(t *testing.T) {
	opts := []permissionOptionRaw{{OptionID: "allow"}, {OptionID: ""}}
	if !knownOptionID(opts, "allow") {
		t.Error("knownOptionID(allow) = false, want true")
	}
	if knownOptionID(opts, "evil") {
		t.Error("knownOptionID(evil) = true, want false")
	}
	if knownOptionID(opts, "") {
		t.Error("knownOptionID(\"\") = true, want false")
	}
}

func TestParsePermissionReply(t *testing.T) {
	cases := map[string]string{
		`[permission_response] option_id="allow"`:      "allow",
		`  [permission_response]  option_id="deny"`:    "deny",
		`[permission_response] option_id="allow-once"`: "allow-once",
		`no marker here`: "",
		`[permission_request] options=[{"id":"x"}]`: "",
	}
	for input, want := range cases {
		if got := parsePermissionReply(input); got != want {
			t.Errorf("parsePermissionReply(%q) = %q, want %q", input, got, want)
		}
	}
}

// ---- fake ACP agent (helper process) ---------------------------------------

// TestFakeACPAgent is a no-op in a normal test run. When spawned as a
// subprocess (FLEETDECK_FAKE_ACP=1) it acts as a scripted ACP agent speaking
// NDJSON on stdio, so the adapter can be exercised without a real agent.
func TestFakeACPAgent(t *testing.T) {
	if os.Getenv("FLEETDECK_FAKE_ACP") != "1" {
		t.Skip("helper process; only runs as a spawned ACP agent")
	}
	runFakeAgent(os.Getenv("FLEETDECK_FAKE_ACP_SCENARIO"))
	os.Exit(0)
}

type fakeRPC struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func runFakeAgent(scenario string) {
	enc := json.NewEncoder(os.Stdout)
	dec := json.NewDecoder(os.Stdin)

	for {
		var msg fakeRPC
		if err := dec.Decode(&msg); err != nil {
			return
		}
		switch msg.Method {
		case "initialize":
			fakeResult(enc, msg.ID, map[string]any{"protocolVersion": 1})
		case "session/new":
			if scenario == "usage" {
				// Mirror what opencode reports: session metadata carries the
				// active model as a config option.
				fakeResult(enc, msg.ID, map[string]any{
					"sessionId": "agent-sess-1",
					"configOptions": []map[string]any{
						{"id": "model", "name": "Model", "category": "model",
							"type": "select", "currentValue": "fake/model-x"},
					},
				})
			} else {
				fakeResult(enc, msg.ID, map[string]any{"sessionId": "agent-sess-1"})
			}
		case "session/load":
			fakeResult(enc, msg.ID, map[string]any{"sessionId": "agent-sess-loaded"})
		case "session/prompt":
			fakePrompt(enc, dec, msg.ID, scenario)
		case "session/cancel":
			// Deliberately ignored: the adapter must kill the process.
		}
	}
}

func fakePrompt(enc *json.Encoder, dec *json.Decoder, id json.RawMessage, scenario string) {
	switch scenario {
	case "deltas":
		fakeNotify(enc, "session/update", fakeThought("m1", "planning the answer"))
		fakeNotify(enc, "session/update", fakeChunk("m1", "Hello"))
		fakeNotify(enc, "session/update", fakeChunk("m1", ", "))
		fakeNotify(enc, "session/update", fakeChunk("m1", "world"))
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "todo":
		fakeNotify(enc, "session/update", fakeTodoUpdate())
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "permission":
		fakeNotify(enc, "session/update", fakeChunk("m1", "starting"))
		fakeRequest(enc, "perm-1", "session/request_permission", fakePermissionParams())
		for {
			var msg fakeRPC
			if err := dec.Decode(&msg); err != nil {
				os.Exit(1)
			}
			if msg.Method != "" || string(msg.ID) != `"perm-1"` {
				continue
			}
			fakeNotify(enc, "session/update", fakeChunk("m2", "perm:"+fakeSelectedOption(msg.Result)))
			break
		}
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "elicitation":
		fakeNotify(enc, "session/update", fakeChunk("m1", "starting"))
		fakeRequest(enc, "elicit-1", "elicitation/create", fakeElicitationParams())
		resp := fakeWaitResponse(dec, "elicit-1")
		if resp.Error != nil {
			fakeNotify(enc, "session/update", fakeChunk("m2", "elicit-error:"+string(resp.Error)))
		} else {
			fakeNotify(enc, "session/update", fakeChunk("m2", "elicit:"+fakeElicitationOutcome(resp.Result)))
		}
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "elicitation-url":
		fakeNotify(enc, "session/update", fakeChunk("m1", "starting"))
		fakeRequest(enc, "elicit-1", "elicitation/create", fakeElicitationParamsURL())
		resp := fakeWaitResponse(dec, "elicit-1")
		fakeNotify(enc, "session/update", fakeChunk("m2", "elicit-error:"+string(resp.Error)))
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "terminal":
		fakeTerminalTurn(enc, dec)
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "terminal-kill":
		fakeTerminalKill(enc, dec)
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	case "crash":
		os.Exit(1)
	case "fs":
		fakeFSTurn(enc, dec, id)
	case "usage", "usage-nomodel":
		// The prompt result carries turn usage exactly like opencode's
		// (input/output/total/thought); the adapter relays input/output/total
		// verbatim.
		fakeResult(enc, id, map[string]any{
			"stopReason": "end_turn",
			"usage": map[string]any{
				"inputTokens": 10, "outputTokens": 5, "totalTokens": 17, "thoughtTokens": 2,
			},
		})
	case "wait":
		// Never answer: the adapter must cancel and kill us.
	default:
		fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
	}
}

// fakeFSTurn drives one read, one write, and one workspace escape through the
// adapter's fs/* handlers, reporting each outcome as a message chunk the Go
// side can assert on.
func fakeFSTurn(enc *json.Encoder, dec *json.Decoder, id json.RawMessage) {
	fakeRequest(enc, "fs-read-1", "fs/read_text_file", map[string]any{"sessionId": "agent-sess-1", "path": "hello.txt"})
	read := fakeWaitResponse(dec, "fs-read-1")
	var readRes struct {
		Content string `json:"content"`
	}
	if read.Error == nil && json.Unmarshal(read.Result, &readRes) == nil {
		fakeNotify(enc, "session/update", fakeChunk("m1", "read:"+readRes.Content))
	} else {
		fakeNotify(enc, "session/update", fakeChunk("m1", "read:err"))
	}

	fakeRequest(enc, "fs-write-1", "fs/write_text_file", map[string]any{
		"sessionId": "agent-sess-1", "path": "out.txt", "content": "written-by-agent",
	})
	if fakeWaitResponse(dec, "fs-write-1").Error == nil {
		fakeNotify(enc, "session/update", fakeChunk("m2", "write:ok"))
	} else {
		fakeNotify(enc, "session/update", fakeChunk("m2", "write:err"))
	}

	fakeRequest(enc, "fs-escape-1", "fs/read_text_file", map[string]any{"sessionId": "agent-sess-1", "path": "../escape.txt"})
	if fakeWaitResponse(dec, "fs-escape-1").Error != nil {
		fakeNotify(enc, "session/update", fakeChunk("m3", "escape:refused"))
	} else {
		fakeNotify(enc, "session/update", fakeChunk("m3", "escape:leaked"))
	}
	fakeResult(enc, id, map[string]any{"stopReason": "end_turn"})
}

// fakeWaitResponse reads stdin until the response with the given id arrives.
func fakeWaitResponse(dec *json.Decoder, wantID string) fakeRPC {
	for {
		var msg fakeRPC
		if err := dec.Decode(&msg); err != nil {
			os.Exit(1)
		}
		if msg.Method == "" && string(msg.ID) == `"`+wantID+`"` {
			return msg
		}
	}
}

func fakeResult(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fakeRequest(enc *json.Encoder, id, method string, params any) {
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func fakeNotify(enc *json.Encoder, method string, params any) {
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func fakeThought(messageID, text string) map[string]any {
	return map[string]any{"sessionId": "agent-sess-1", "update": map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"messageId":     messageID,
		"content":       map[string]any{"type": "text", "text": text},
	}}
}

func fakeChunk(messageID, text string) map[string]any {
	return map[string]any{"sessionId": "agent-sess-1", "update": map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     messageID,
		"content":       map[string]any{"type": "text", "text": text},
	}}
}

func fakeTodoUpdate() map[string]any {
	return map[string]any{"sessionId": "agent-sess-1", "update": map[string]any{
		"sessionUpdate": "tool_call",
		"title":         "todo_write",
		"kind":          "other",
		"rawInput": map[string]any{"todos": []map[string]any{
			{"content": "Research", "status": "completed"},
			{"content": "Implement", "status": "in_progress"},
			{"content": "Review", "status": "pending"},
		}},
	}}
}

func fakeTerminalTurn(enc *json.Encoder, dec *json.Decoder) {
	fakeRequest(enc, "term-create", "terminal/create", map[string]any{
		"sessionId":       "agent-sess-1",
		"command":         "sh",
		"args":            []string{"-c", "printf hello-from-terminal"},
		"outputByteLimit": 64,
	})
	created := fakeWaitResponse(dec, "term-create")
	var createResult struct {
		TerminalID string `json:"terminalId"`
	}
	if created.Error != nil || json.Unmarshal(created.Result, &createResult) != nil || createResult.TerminalID == "" {
		fakeNotify(enc, "session/update", fakeChunk("m1", "terminal:create-failed"))
		return
	}

	output := ""
	exited := false
	for range 100 {
		fakeRequest(enc, "term-output", "terminal/output", map[string]any{
			"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
		})
		resp := fakeWaitResponse(dec, "term-output")
		var result struct {
			Output     string          `json:"output"`
			Truncated  bool            `json:"truncated"`
			ExitStatus json.RawMessage `json:"exitStatus"`
		}
		if resp.Error == nil && json.Unmarshal(resp.Result, &result) == nil {
			output = result.Output
			exited = len(result.ExitStatus) > 0
		}
		if exited {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	fakeRequest(enc, "term-wait", "terminal/wait_for_exit", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	waited := fakeWaitResponse(dec, "term-wait")
	var waitResult struct {
		ExitCode *int `json:"exitCode"`
	}
	_ = json.Unmarshal(waited.Result, &waitResult)

	fakeRequest(enc, "term-release", "terminal/release", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	released := fakeWaitResponse(dec, "term-release")
	releasedOK := released.Error == nil

	// A released id is invalid for every other terminal method.
	fakeRequest(enc, "term-after-release", "terminal/output", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	after := fakeWaitResponse(dec, "term-after-release")
	summary := fmt.Sprintf("terminal:%s:exit=%v:released=%v:invalid-after=%v",
		output, waitResult.ExitCode != nil && *waitResult.ExitCode == 0, releasedOK, after.Error != nil)
	fakeNotify(enc, "session/update", fakeChunk("m1", summary))
}

func fakeTerminalKill(enc *json.Encoder, dec *json.Decoder) {
	fakeRequest(enc, "term-create", "terminal/create", map[string]any{
		"sessionId": "agent-sess-1",
		"command":   "sleep",
		"args":      []string{"30"},
	})
	created := fakeWaitResponse(dec, "term-create")
	var createResult struct {
		TerminalID string `json:"terminalId"`
	}
	if created.Error != nil || json.Unmarshal(created.Result, &createResult) != nil || createResult.TerminalID == "" {
		fakeNotify(enc, "session/update", fakeChunk("m1", "terminal:create-failed"))
		return
	}

	fakeRequest(enc, "term-kill", "terminal/kill", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	killed := fakeWaitResponse(dec, "term-kill")

	fakeRequest(enc, "term-wait", "terminal/wait_for_exit", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	waited := fakeWaitResponse(dec, "term-wait")
	var waitResult struct {
		ExitCode *int    `json:"exitCode"`
		Signal   *string `json:"signal"`
	}
	_ = json.Unmarshal(waited.Result, &waitResult)

	fakeRequest(enc, "term-release", "terminal/release", map[string]any{
		"sessionId": "agent-sess-1", "terminalId": createResult.TerminalID,
	})
	released := fakeWaitResponse(dec, "term-release")

	signal := ""
	if waitResult.Signal != nil {
		signal = *waitResult.Signal
	}
	summary := fmt.Sprintf("terminal-kill:killed=%v:signal=%s:released=%v",
		killed.Error == nil, signal, released.Error == nil)
	fakeNotify(enc, "session/update", fakeChunk("m1", summary))
}

func fakeElicitationParams() map[string]any {
	return map[string]any{
		"sessionId": "agent-sess-1",
		"mode":      "form",
		"message":   "How should I proceed?",
		"requestedSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
			},
			"required": []string{"value"},
		},
	}
}

func fakeElicitationParamsURL() map[string]any {
	return map[string]any{
		"sessionId":     "agent-sess-1",
		"mode":          "url",
		"message":       "Authorize",
		"url":           "https://example.com/oauth",
		"elicitationId": "elicit-url-1",
	}
}

func fakeElicitationOutcome(raw json.RawMessage) string {
	var r struct {
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if len(r.Content) == 0 {
		return r.Action
	}
	return r.Action + ":" + string(r.Content)
}

func fakePermissionParams() map[string]any {
	return map[string]any{
		"sessionId": "agent-sess-1",
		"toolCall":  map[string]any{"title": "Run shell command"},
		"options": []map[string]any{
			{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
		},
	}
}

func fakeSelectedOption(raw json.RawMessage) string {
	var r struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if r.Outcome.Outcome != "selected" {
		return r.Outcome.Outcome
	}
	return r.Outcome.OptionID
}
