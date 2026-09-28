// Package acp implements the shared Agent Client Protocol (ACP) adapter used
// for the codex, claude, opencode, and pi agent types. Each type is registered
// separately in adapters.Default but shares this one implementation, differing
// only in how the agent process is launched.
//
// # ACP wire convention
//
// The agent is launched as a subprocess and speaks JSON-RPC 2.0 over
// newline-delimited JSON (NDJSON) on stdio: one JSON object per line, in both
// directions, with no embedded newlines. The spec reserves stderr for agent
// logs (never protocol frames); this adapter discards it — see process.go.
//
//	client -> agent requests   : initialize, session/new, session/load, session/prompt, authenticate
//	client -> agent notification: session/cancel
//	agent  -> client request   : session/request_permission, elicitation/create, fs/*, terminal/*
//	agent  -> client notification: session/update (agent_message_chunk, agent_thought_chunk,
//	                               tool_call, tool_call_update, plan, ...)
//
// This adapter serves session/request_permission and fs/read_text_file /
// fs/write_text_file (workspace-sandboxed, fs.go); elicitation and terminal/*
// are refused and not advertised.
//
// Permission flow (locked Decision 2)
//
// When the agent asks for permission, the adapter pauses the turn and asks the
// human. The prompt handed to EventSink.UserInputRequested is:
//
//	<question>
//
//	[permission_request] options=[{"id":"allow-once","label":"Allow"},{"id":"deny","label":"Deny"}]
//
// The web replies through the normal SendMessage path with a line of the form:
//
//	[permission_response] option_id="allow-once"
//
// The adapter unwraps the option id and answers ACP with that `optionId`. If
// the reply cannot be unwrapped, the adapter defaults to a deny option and
// logs. Permission requests are never auto-approved.
//
// Todo mirror (locked Decision 5)
//
// ACP has a native `plan` session update, but not every agent emits it (pi does
// not). The adapter therefore mirrors todos from either source:
//
//   - a `plan` update's entries[] map directly to protocol.Todo; and
//   - a `tool_call`/`tool_call_update` whose title matches
//     `(?i)^(todowrite|todo_write|update_plan|plan)\b` (the `todo_write`
//     heuristic) is parsed for a `todos`/`items`/`plan` array in its rawInput.
//
// Unparseable payloads are skipped silently: a todo mirror must never break a
// running session.
//
// # Usage & model capture
//
// Evidence (raw frames from `opencode acp`, scripts/smoke_acp_opencode.sh):
//
//   - usage: each `session/prompt` result carries
//     `usage: {inputTokens, outputTokens, totalTokens, thoughtTokens}`. The
//     mapping onto adapters.Usage is inputTokens -> PromptTokens,
//     outputTokens -> CompletionTokens, totalTokens -> TotalTokens, verbatim.
//     Observed opencode arithmetic: totalTokens = input + output + thought, so
//     prompt + completion may not sum to total; the agent's total is kept as
//     reported. A result without a `usage` field reports nothing (zero rows) —
//     numbers are never fabricated.
//   - model: `session/new` (and `session/load`) results carry
//     `configOptions: [{id: "model", currentValue: "<provider/model>", ...}]`.
//     That currentValue is the model the agent actually runs — the best real
//     source. When an agent reports no model config option (the ACP spec makes
//     it optional and other agents may omit it), the documented fallback is
//     the agent_type (e.g. "codex", "opencode").
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// defaultCancelGrace is how long Cancel waits after sending session/cancel
// before forcibly killing the agent process.
const defaultCancelGrace = 2 * time.Second

// mcpServer is a neutral MCP server descriptor passed through verbatim to ACP
// `session/new` / `session/load`. v1 leaves this empty: FleetDeck's MCP server
// is configured agent-side, not injected over ACP (see the Phase 4 brief).
type mcpServer map[string]any

// agentConfig is the per-agent-type launch recipe.
type agentConfig struct {
	command    string
	args       []string
	env        []string
	mcpServers []mcpServer
}

// agentInventory maps an agent_type to the canonical ACP launcher from the
// Phase 4 brief. claude/codex/pi are npm-distributed launchers run through npx;
// opencode ships its own binary with an `acp` subcommand.
var agentInventory = map[string]agentConfig{
	"codex": {
		command: "npx",
		args:    []string{"-y", "@agentclientprotocol/codex-acp"},
	},
	"claude": {
		command: "npx",
		args:    []string{"-y", "@agentclientprotocol/claude-agent-acp"},
		// ANTHROPIC_API_KEY (and friends) are inherited from the process env.
	},
	"opencode": {
		command: "opencode",
		args:    []string{"acp"},
	},
	"pi": {
		command: "npx",
		args:    []string{"-y", "pi-acp"},
		// Requires pi >= 0.81.0 on PATH; `pi-acp` drives `pi --mode rpc`.
	},
}

// Adapter is the shared ACP Adapter implementation. One Adapter drives one
// session; adapters.Default constructs a fresh one per session via New.
type Adapter struct {
	// ResumeSessionID, when non-empty, makes Start reattach to an existing
	// agent session with `session/load` instead of creating one with
	// `session/new`. It is also reported as StartResult.ExternalSessionID.
	//
	// Prefer driving this through adapters.StartRequest.ResumeSessionID (it
	// copies over at the top of Start); the field stays settable directly for
	// existing callers. Either way it must be fixed before Start runs.
	ResumeSessionID string

	// CancelGrace overrides the 2s grace between `session/cancel` and the hard
	// kill. Tests set a small value; zero means defaultCancelGrace.
	CancelGrace time.Duration

	agentType string
	cfg       agentConfig
	log       *slog.Logger

	mu           sync.Mutex
	started      bool
	sessionID    string
	externalID   string
	workspace    string
	model        string // agent-reported model (session metadata); see modelOrAgentType
	status       protocol.SessionStatus
	proc         *process
	awaitingPerm chan string
	sink         adapters.EventSink

	cancelCh   chan struct{}
	cancelOnce sync.Once
	finishOnce sync.Once
	committed  atomic.Bool

	nextID atomic.Int64
	msgSeq atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse

	accMu sync.Mutex
	acc   strings.Builder
}

var _ adapters.Adapter = (*Adapter)(nil)

// New returns an ACP Adapter for agentType. Unknown types yield a zero config;
// Start then fails with a clear "no configuration" error rather than silently
// doing nothing.
func New(agentType string) *Adapter {
	return &Adapter{
		agentType: agentType,
		cfg:       agentInventory[agentType],
		log:       slog.Default().With("adapter", "acp", "agent_type", agentType),
	}
}

// SetResumeSessionID implements adapters.Resumable: the next Start reattaches
// to the given agent-side session with `session/load` instead of creating one
// with `session/new`.
func (a *Adapter) SetResumeSessionID(externalSessionID string) {
	a.ResumeSessionID = externalSessionID
}

// Start spawns the agent, completes the ACP handshake (initialize, then
// session/new or session/load), emits StateChange(running), and kicks off the
// first turn with the task prompt. It returns once the session is established;
// the read goroutine drives further events through sink until Done/Error.
//
// Resume: StartRequest.ResumeSessionID selects the session/load path, and the
// prompt runs as the first turn on the reattached session — so a resume needs
// no follow-up SendMessage: Start(prompt=message) already ends with the agent
// answering the follow-up, exactly like the initial Start(prompt) turn (see
// TestSessionResume).
func (a *Adapter) Start(ctx context.Context, req adapters.StartRequest, sink adapters.EventSink) (adapters.StartResult, error) {
	if req.ResumeSessionID != "" {
		a.ResumeSessionID = req.ResumeSessionID
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return adapters.StartResult{}, fmt.Errorf("acp: adapter for session %q already started", a.sessionID)
	}
	a.started = true
	a.sessionID = req.SessionID
	a.workspace = req.WorkspacePath
	a.status = protocol.SessionWaiting
	a.sink = sink
	a.cancelCh = make(chan struct{})
	a.pending = make(map[int64]chan rpcResponse)
	a.mu.Unlock()

	command := a.cfg.command
	if command == "" {
		return adapters.StartResult{}, fmt.Errorf("acp: no launch configuration for agent type %q", a.agentType)
	}

	env := append(os.Environ(), a.cfg.env...)
	proc, err := startProcess(ctx, command, a.cfg.args, env, req.WorkspacePath)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			a.log.Warn("acp agent binary not found", "command", command)
			return adapters.StartResult{}, fmt.Errorf("acp: binary for %q not found: %w", a.agentType, exec.ErrNotFound)
		}
		return adapters.StartResult{}, fmt.Errorf("acp: start %q: %w", a.agentType, err)
	}

	a.mu.Lock()
	a.proc = proc
	a.mu.Unlock()

	go a.readLoop(ctx, req.SessionID, sink)
	go a.exitWatcher(ctx, req.SessionID, sink, proc)

	if _, err := a.call(ctx, "initialize", initializeParams{
		ProtocolVersion: 1,
		ClientCapabilities: clientCapabilities{
			FS: fsCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
		ClientInfo: clientInfo{Name: "FleetDeck", Version: "0.1.0"},
	}); err != nil {
		a.shutdown()
		return adapters.StartResult{}, fmt.Errorf("acp: initialize: %w", err)
	}

	externalID, err := a.openSession(ctx, req.WorkspacePath)
	if err != nil {
		a.shutdown()
		return adapters.StartResult{}, err
	}

	a.mu.Lock()
	a.externalID = externalID
	a.status = protocol.SessionRunning
	a.mu.Unlock()

	sink.StateChange(ctx, req.SessionID, protocol.SessionRunning)

	a.committed.Store(true)

	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		if err := a.promptTurn(ctx, sink, prompt); err != nil {
			a.shutdown()
			return adapters.StartResult{}, fmt.Errorf("acp: prompt: %w", err)
		}
	}

	return adapters.StartResult{ExternalSessionID: externalID}, nil
}

// openSession performs session/new (or session/load when resuming) and returns
// the agent-side session id. The result's configOptions are also mined for the
// agent-reported model (see captureModel).
func (a *Adapter) openSession(ctx context.Context, workspace string) (string, error) {
	servers := a.cfg.mcpServers
	if servers == nil {
		servers = []mcpServer{}
	}

	if a.ResumeSessionID != "" {
		raw, err := a.call(ctx, "session/load", sessionLoadParams{
			SessionID:  a.ResumeSessionID,
			Cwd:        workspace,
			MCPServers: servers,
		})
		if err != nil {
			return "", fmt.Errorf("acp: session/load: %w", err)
		}
		a.captureModel(raw)
		return a.ResumeSessionID, nil
	}

	raw, err := a.call(ctx, "session/new", sessionNewParams{Cwd: workspace, MCPServers: servers})
	if err != nil {
		return "", fmt.Errorf("acp: session/new: %w", err)
	}
	var res sessionOpenResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("acp: decode session/new: %w", err)
	}
	if res.SessionID == "" {
		return "", errors.New("acp: session/new returned no sessionId")
	}
	a.captureModel(raw)
	return res.SessionID, nil
}

// sessionOpenResult is the shape shared by session/new and session/load
// results: the agent-side session id plus optional session metadata.
type sessionOpenResult struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []configOption `json:"configOptions"`
}

// configOption is one agent-reported session configuration entry. A select
// option carries its active choice in CurrentValue (other kinds may carry
// other JSON types, hence any).
type configOption struct {
	ID           string `json:"id"`
	CurrentValue any    `json:"currentValue"`
}

// captureModel records the model the agent reports in its session metadata
// (configOptions entry id "model"). Agents that report no model config option
// leave it unset; modelOrAgentType then falls back to the agent_type.
func (a *Adapter) captureModel(raw json.RawMessage) {
	var res sessionOpenResult
	if json.Unmarshal(raw, &res) != nil {
		return
	}
	for _, opt := range res.ConfigOptions {
		if opt.ID != "model" {
			continue
		}
		if s, ok := opt.CurrentValue.(string); ok && s != "" {
			a.mu.Lock()
			a.model = s
			a.mu.Unlock()
			return
		}
	}
}

// modelOrAgentType returns the agent-reported model, falling back to the
// agent_type when the agent reported none (documented in the package comment).
func (a *Adapter) modelOrAgentType() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model != "" {
		return a.model
	}
	return a.agentType
}

// SendMessage either answers a pending permission request (when one is open) or
// starts a new turn with the user's text.
func (a *Adapter) SendMessage(ctx context.Context, sessionID string, message string) error {
	a.mu.Lock()
	if !a.started || a.sessionID != sessionID {
		a.mu.Unlock()
		return fmt.Errorf("acp: unknown session %q", sessionID)
	}
	perm := a.awaitingPerm
	sink := a.sink
	a.mu.Unlock()

	if perm != nil {
		// A turn is paused on a permission request: this message is the reply.
		select {
		case perm <- message:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-a.cancelCh:
			return context.Canceled
		}
	}

	a.setStatus(protocol.SessionRunning)
	if sink != nil {
		sink.StateChange(ctx, sessionID, protocol.SessionRunning)
	}
	return a.promptTurn(ctx, sink, message)
}

// Cancel asks the agent to cancel the current turn, then hard-kills it after a
// grace period. It returns once the process has exited (or ctx is done). Done
// is emitted by the exit watcher.
func (a *Adapter) Cancel(ctx context.Context, sessionID string) error {
	a.mu.Lock()
	if !a.started || a.sessionID != sessionID {
		a.mu.Unlock()
		return fmt.Errorf("acp: unknown session %q", sessionID)
	}
	proc := a.proc
	external := a.externalID
	a.mu.Unlock()

	a.cancelOnce.Do(func() { close(a.cancelCh) })

	if proc == nil {
		return nil
	}

	_ = proc.writeLine(rpcNotification{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params:  cancelParams{SessionID: external},
	})

	grace := a.CancelGrace
	if grace <= 0 {
		grace = defaultCancelGrace
	}
	go func() {
		select {
		case <-proc.done:
		case <-time.After(grace):
			proc.kill()
		}
	}()

	select {
	case <-proc.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(grace + 2*time.Second):
	}
	return nil
}

// ---- handshake params ------------------------------------------------------

type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         clientInfo         `json:"clientInfo"`
}

// clientCapabilities advertises exactly what this adapter serves: workspace-
// sandboxed fs read/write (fs.go). terminal/* and elicitation are deliberately
// absent — claiming unimplemented capabilities would let an agent hang on a
// call that never answers, and the refusals in handleAgentRequest exist only
// as a safety net for agents that call unadvertised methods anyway.
type clientCapabilities struct {
	FS fsCapabilities `json:"fs"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type sessionNewParams struct {
	Cwd        string      `json:"cwd"`
	MCPServers []mcpServer `json:"mcpServers"`
}

type sessionLoadParams struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	MCPServers []mcpServer `json:"mcpServers"`
}

type sessionPromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type cancelParams struct {
	SessionID string `json:"sessionId"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ---- JSON-RPC framing ------------------------------------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponseOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("acp: rpc error %d: %s", e.Code, e.Message)
}

// inbound is any ACP line from the agent (request, notification, or response).
type inbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func hasID(id json.RawMessage) bool {
	return len(id) > 0 && string(id) != "null"
}

func (m inbound) isRequest() bool      { return m.Method != "" && hasID(m.ID) }
func (m inbound) isNotification() bool { return m.Method != "" && !hasID(m.ID) }
func (m inbound) isResponse() bool     { return m.Method == "" && hasID(m.ID) }

type rpcResponse struct {
	result json.RawMessage
	err    *rpcError
}

// call sends a request and blocks until its response arrives, the agent exits,
// or ctx/cancel fires.
func (a *Adapter) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return nil, errors.New("acp: no agent process")
	}

	id := a.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	if err := proc.writeLine(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.err != nil {
			return nil, resp.err
		}
		return resp.result, nil
	case <-proc.done:
		return nil, fmt.Errorf("acp: agent exited before responding to %s", method)
	case <-a.cancelCh:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// promptTurn sends session/prompt and handles its response asynchronously: the
// turn ends when the agent returns a stopReason.
func (a *Adapter) promptTurn(ctx context.Context, sink adapters.EventSink, text string) error {
	a.mu.Lock()
	proc := a.proc
	external := a.externalID
	a.mu.Unlock()
	if proc == nil {
		return errors.New("acp: no agent process")
	}

	id := a.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	if err := proc.writeLine(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		Params: sessionPromptParams{
			SessionID: external,
			Prompt:    []contentBlock{{Type: "text", Text: text}},
		},
	}); err != nil {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
		return err
	}

	// The pending entry must outlive this call: it is removed by the goroutine
	// once the turn's response (or a terminal condition) arrives.
	go func() {
		defer func() {
			a.pendingMu.Lock()
			delete(a.pending, id)
			a.pendingMu.Unlock()
		}()
		select {
		case resp := <-ch:
			a.handlePromptResult(ctx, sink, resp)
		case <-a.cancelCh:
		case <-proc.done:
		case <-ctx.Done():
		}
	}()
	return nil
}

// promptResult is the decoded session/prompt result. Usage is what the agent
// reported for this turn (see the package comment); nil means "reported no
// usage" and must stay unrecorded.
type promptResult struct {
	StopReason string       `json:"stopReason"`
	Usage      *usageReport `json:"usage"`
}

// usageReport is one turn's token accounting as carried in a session/prompt
// result (observed from opencode: inputTokens/outputTokens/totalTokens/
// thoughtTokens). Fields are relayed verbatim; the agent's totalTokens is kept
// even though it may include thoughtTokens beyond input + output.
type usageReport struct {
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	TotalTokens   int64 `json:"totalTokens"`
	ThoughtTokens int64 `json:"thoughtTokens"`
}

func (a *Adapter) handlePromptResult(ctx context.Context, sink adapters.EventSink, resp rpcResponse) {
	if sink == nil {
		return
	}
	if resp.err != nil {
		a.terminate(ctx, sink, fmt.Errorf("acp: prompt failed: %w", resp.err))
		return
	}

	var res promptResult
	_ = json.Unmarshal(resp.result, &res)
	if res.Usage != nil {
		sink.Usage(ctx, a.currentSessionID(), adapters.Usage{
			Model:            a.modelOrAgentType(),
			PromptTokens:     res.Usage.InputTokens,
			CompletionTokens: res.Usage.OutputTokens,
			TotalTokens:      res.Usage.TotalTokens,
		})
	}
	switch res.StopReason {
	case "end_turn", "cancelled", "":
		a.emitFinalMessage(ctx, sink)
		a.terminate(ctx, sink, nil)
	case "refusal", "max_tokens", "max_turn_requests":
		a.emitFinalMessage(ctx, sink)
		a.terminate(ctx, sink, fmt.Errorf("acp: prompt stopped: %s", res.StopReason))
	default:
		a.emitFinalMessage(ctx, sink)
		a.terminate(ctx, sink, nil)
	}
}

// ---- read loop -------------------------------------------------------------

func (a *Adapter) readLoop(ctx context.Context, sessionID string, sink adapters.EventSink) {
	for {
		a.mu.Lock()
		proc := a.proc
		a.mu.Unlock()
		if proc == nil {
			return
		}

		line, err := proc.readLine()
		if err != nil {
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var msg inbound
		if json.Unmarshal(line, &msg) != nil {
			continue // ignore malformed lines rather than tear down the session
		}

		switch {
		case msg.isRequest():
			a.handleAgentRequest(ctx, sessionID, sink, msg)
		case msg.isNotification():
			a.handleNotification(ctx, sessionID, sink, msg)
		case msg.isResponse():
			a.deliverResponse(msg)
		}
	}
}

func (a *Adapter) deliverResponse(msg inbound) {
	var id int64
	if json.Unmarshal(msg.ID, &id) != nil {
		return
	}
	a.pendingMu.Lock()
	ch := a.pending[id]
	a.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- rpcResponse{result: msg.Result, err: msg.Error}:
	default:
	}
}

// handleAgentRequest answers the client methods the agent may call. Supported:
// session/request_permission (the pause-and-pick flow) and the workspace-
// sandboxed fs/read_text_file / fs/write_text_file. terminal/* and the
// elicitation API are refused with method-not-found — they are not advertised
// in initialize, so a well-behaved agent never calls them; refusing instead of
// hanging keeps a misbehaving agent from stalling the session.
func (a *Adapter) handleAgentRequest(ctx context.Context, sessionID string, sink adapters.EventSink, msg inbound) {
	switch {
	case msg.Method == "session/request_permission":
		a.handlePermissionRequest(ctx, sessionID, sink, msg)
	case msg.Method == "fs/read_text_file" || msg.Method == "fs/write_text_file":
		a.handleFSRequest(msg)
	default:
		a.respond(msg.ID, nil, &rpcError{Code: -32601, Message: "method not supported: " + msg.Method})
	}
}

// ---- session updates -------------------------------------------------------

type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	MessageID     string          `json:"messageId"`
	Content       contentBlock    `json:"content"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	RawInput      json.RawMessage `json:"rawInput"`
	Entries       []planEntry     `json:"entries"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

func (a *Adapter) handleNotification(ctx context.Context, sessionID string, sink adapters.EventSink, msg inbound) {
	if msg.Method != "session/update" {
		return
	}
	var p sessionUpdateParams
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}

	switch p.Update.SessionUpdate {
	case "agent_message_chunk":
		if text := p.Update.Content.Text; text != "" {
			a.appendText(text)
			if sink != nil {
				sink.TextDelta(ctx, sessionID, text)
			}
		}
	case "agent_thought_chunk":
		// Thoughts are deliberately not surfaced as TextDelta.
	case "plan":
		if todos := parsePlan(p.Update.Entries); len(todos) > 0 && sink != nil {
			sink.TodoUpdate(ctx, sessionID, todos)
		}
	case "tool_call", "tool_call_update":
		if todos := parseTodoToolCall(p.Update.Title, p.Update.RawInput); len(todos) > 0 && sink != nil {
			sink.TodoUpdate(ctx, sessionID, todos)
		}
	}
}

// ---- lifecycle helpers -----------------------------------------------------

// exitWatcher emits the terminal event exactly once when the agent process
// exits on its own. During the handshake (not yet committed) it stays silent:
// Start reports the error instead.
func (a *Adapter) exitWatcher(ctx context.Context, sessionID string, sink adapters.EventSink, proc *process) {
	<-proc.done

	a.mu.Lock()
	status := a.status
	a.mu.Unlock()

	select {
	case <-a.cancelCh:
		a.terminate(ctx, sink, nil)
		return
	default:
	}

	if status == protocol.SessionDone || status == protocol.SessionFailed {
		return
	}
	if proc.waitErr != nil {
		a.terminate(ctx, sink, fmt.Errorf("acp: agent exited: %w", proc.waitErr))
		return
	}
	a.terminate(ctx, sink, nil)
}

// terminate reports the terminal outcome exactly once and reaps the process.
// Done and Error carry the final state themselves (see EventSink), so no
// separate StateChange is emitted. It is a no-op before Start commits.
func (a *Adapter) terminate(ctx context.Context, sink adapters.EventSink, err error) {
	if !a.committed.Load() {
		a.killProc()
		return
	}
	a.finishOnce.Do(func() {
		status := protocol.SessionDone
		if err != nil {
			status = protocol.SessionFailed
		}
		a.mu.Lock()
		a.status = status
		a.mu.Unlock()

		if sink != nil {
			sessionID := a.currentSessionID()
			if err != nil {
				sink.Error(ctx, sessionID, err)
			} else {
				sink.Done(ctx, sessionID)
			}
		}
		a.killProc()
	})
}

func (a *Adapter) killProc() {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return
	}
	proc.closeStdin()
	proc.kill()
}

// shutdown reaps the process after a failed Start.
func (a *Adapter) shutdown() {
	a.killProc()
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return
	}
	select {
	case <-proc.done:
	case <-time.After(time.Second):
	}
}

func (a *Adapter) appendText(text string) {
	a.accMu.Lock()
	a.acc.WriteString(text)
	a.accMu.Unlock()
}

// emitFinalMessage flushes the accumulated agent text as one assistant
// Message and resets the accumulator for the next turn.
func (a *Adapter) emitFinalMessage(ctx context.Context, sink adapters.EventSink) {
	if sink == nil {
		return
	}
	a.accMu.Lock()
	content := a.acc.String()
	a.acc.Reset()
	a.accMu.Unlock()
	if strings.TrimSpace(content) == "" {
		return
	}
	sessionID := a.currentSessionID()
	sink.Message(ctx, sessionID, protocol.Message{
		ID:        fmt.Sprintf("%s-msg-%d", sessionID, a.msgSeq.Add(1)),
		SessionID: sessionID,
		Role:      protocol.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	})
}

func (a *Adapter) currentSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

func (a *Adapter) currentWorkspace() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workspace
}

func (a *Adapter) setStatus(status protocol.SessionStatus) {
	a.mu.Lock()
	a.status = status
	a.mu.Unlock()
}
