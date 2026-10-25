// Package acp implements the shared Agent Client Protocol (ACP) adapter used
// for the codex, claude, opencode, pi, and omp agent types. Each type is
// registered separately in adapters.Default but shares this one implementation,
// differing only in how the agent process is launched.
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
//	agent  -> client request   : session/request_permission, elicitation/create (form), fs/*, terminal/*
//	agent  -> client notification: session/update (agent_message_chunk, agent_thought_chunk,
//	                               tool_call, tool_call_update, plan, ...)
//
// This adapter serves session/request_permission, form-mode elicitation
// (elicitation/create), fs/read_text_file / fs/write_text_file
// (workspace-sandboxed, fs.go), and the five terminal/* methods (terminal.go).
// URL-mode elicitation is refused and not advertised.
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
// logs. An unanswered request is never assumed to be approved.
//
// Auto-allow: when the owning workspace carries an auto-allow range
// (AutoAllowScope), the adapter may answer the request itself with the
// least-privilege `allow_once` option instead of pausing. "workspace" approves
// requests whose reported locations (toolCall.locations) all resolve inside
// the workspace, and treats a request that reports no locations (an opaque
// shell command) as inside; "all" approves every request. "none" always asks.
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
// # Session title
//
// `session_info_update` carries the title the agent assigns to its session. It
// is relayed through the optional adapters.TitleSink so the hub can backfill a
// provisional task title; replay runs never emit it. Sources differ per
// launcher: codex-acp generates a title in a fire-and-forget turn after
// answering session/prompt (the process is kept alive for titleGrace so the
// update is not lost), opencode/pi never name a session, and omp's ACP mode
// forces PI_NO_TITLE (its automatic naming is TUI-only). omp is therefore
// asked with its `/rename` built-in after the final turn when the hub says the
// task title is still provisional; that internal turn's output is suppressed
// and the task is already reported done while it runs.
//
// # Usage & model capture
//
// Evidence (raw frames from `opencode acp`, scripts/smoke_acp_opencode.sh):
//
//   - usage: each `session/prompt` result carries
//     `usage: {inputTokens, outputTokens, totalTokens, thoughtTokens}` plus,
//     when the provider exposes prompt caching,
//     `cachedReadTokens`/`cachedWriteTokens`. The mapping onto
//     adapters.Usage is inputTokens -> PromptTokens,
//     outputTokens -> CompletionTokens, totalTokens -> TotalTokens, and the
//     cache counters verbatim (omitted when the agent reports neither).
//     Observed opencode arithmetic: totalTokens = input + output + thought, so
//     prompt + completion may not sum to total; the agent's total is kept as
//     reported. inputTokens is the non-cached input portion, so the full input
//     the model processed is input + cached read + cached write.
//     A result without a `usage` field reports nothing (zero rows) —
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

// titleRequestTimeout bounds the wait for an agent-named title after the
// session's final turn. The task is already reported done when the request
// starts, so this only keeps the agent process around: omp's default fork
// title generator runs a real model turn and can take a couple of minutes.
const titleRequestTimeout = 3 * time.Minute

// titleResponseGrace is the extra wait after the title request's own response
// for a `session_info_update` that the agent emits just behind it.
const titleResponseGrace = 2 * time.Second

// maxElicitationSchemaBytes bounds an agent-supplied elicitation schema before
// it is mirrored into the hub's text buffer as a pending-input prompt.
const maxElicitationSchemaBytes = 64 << 10

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
	// requires are extra executables the launcher shells out to (beyond
	// command itself), used by Requirements.
	requires []string
	// titlePrompt asks the agent to name its session when the task title is
	// still provisional. omp accepts its built-in `/rename` command with no
	// argument as "generate a title"; its ACP mode disables automatic titles
	// (PI_NO_TITLE), so this is the only way to get one. Empty means the
	// launcher has no such command.
	titlePrompt string
	// titleGrace keeps the agent process alive this long after the final turn
	// so a title the agent names on its own still reaches the hub. codex-acp
	// generates its title in a fire-and-forget turn after the prompt response;
	// without the grace the process is killed before the update arrives.
	titleGrace time.Duration
}

// agentInventory maps an agent_type to the canonical ACP launcher from the
// Phase 4 brief. claude/codex/pi are npm-distributed launchers run through npx;
// opencode and omp ship their own binaries with an `acp` subcommand.
var agentInventory = map[string]agentConfig{
	"codex": {
		command: "npx",
		args:    []string{"-y", "@agentclientprotocol/codex-acp"},
		// codex-acp names the thread itself once the first turn completes,
		// after it has answered session/prompt.
		titleGrace: 15 * time.Second,
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
		requires: []string{"pi"},
	},
	"omp": {
		command: "omp",
		args:    []string{"acp"},
		// omp forces PI_NO_TITLE for ACP sessions, but its `/rename` built-in
		// with no argument still generates a title and reports it through
		// session_info_update.
		titlePrompt: "/rename",
	},
}

// Requirements returns the executables agentType is launched through: its ACP
// launcher plus anything that launcher shells out to. It is empty for agent
// types the node runs in-process (e.g. fake) and for unknown types.
func Requirements(agentType string) []string {
	cfg, ok := agentInventory[agentType]
	if !ok {
		return nil
	}
	return append([]string{cfg.command}, cfg.requires...)
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

	mu             sync.Mutex
	started        bool
	sessionID      string
	externalID     string
	workspace      string
	autoAllowScope protocol.AutoAllowScope
	model          string // agent-reported model (session metadata); see modelOrAgentType
	status         protocol.SessionStatus
	proc           *process
	awaitingPerm   chan string
	awaitingElicit chan string
	// pendingAsk is the prompt of the outstanding ask (permission or
	// elicitation) so the node can re-emit it after a reconnect.
	pendingAsk string
	terminals  *terminalManager
	sink       adapters.EventSink

	cancelCh   chan struct{}
	cancelOnce sync.Once
	// turnPending marks a session/prompt whose response has not arrived yet.
	// An agent that exits while true died mid-turn: reporting done would hide
	// the failure behind an empty transcript.
	turnPending atomic.Bool
	// turnActivity is set by any meaningful update of the current turn (text,
	// reasoning, todos, tool calls). A turn that ends with none of them is a
	// failure, not a quiet success: pi-acp, for example, returns end_turn with
	// nothing when its model is misconfigured.
	turnActivity atomic.Bool
	finishOnce   sync.Once
	committed    atomic.Bool

	nextID atomic.Int64
	msgSeq atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse

	accMu sync.Mutex
	acc   strings.Builder

	// replayAsm, when set before Start, routes replayed session/update chunks
	// to the history assembler instead of the live event path (see replay.go).
	// It is write-once per adapter instance, before the read loop starts.
	replayAsm *replayAssembler
	// loading marks the session/load window of a live resume. ACP replays the
	// whole conversation as session/update notifications before the load
	// response; that is history, not this turn's output, so it must not reach
	// the sink or the turn accumulator. (The history proxy sets replayAsm
	// instead, collecting the same chunks.)
	loading atomic.Bool

	// wantTitle records that the hub still has a provisional task title and
	// the launcher can supply the agent's own; the first finished turn
	// consumes it. titleDone tracks whether a non-empty title was relayed, so
	// a request is skipped when the agent already named the session.
	wantTitle atomic.Bool
	titleDone atomic.Bool
	// titleChecked makes the post-turn title collection run at most once per
	// adapter (a follow-up turn must not repeat it).
	titleChecked atomic.Bool
	// titleTurn marks a title-only window: the adapter-issued title command
	// (omp) or the post-turn grace (codex-acp). Its output is not the task's
	// answer (omp prints "Session renamed to ..."), so every update except the
	// title itself is dropped while it runs.
	titleTurn atomic.Bool
	// titleCh is signalled when the first title arrives, waking the post-turn
	// wait.
	titleCh chan struct{}
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
		terminals: newTerminalManager(),
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
	a.autoAllowScope = req.AutoAllowScope
	a.status = protocol.SessionWaiting
	a.sink = sink
	a.cancelCh = make(chan struct{})
	a.pending = make(map[int64]chan rpcResponse)
	a.titleCh = make(chan struct{}, 1)
	a.wantTitle.Store(req.AutoTitle && a.cfg.titlePrompt != "")
	a.mu.Unlock()

	command := a.cfg.command
	if command == "" {
		return adapters.StartResult{}, fmt.Errorf("acp: no launch configuration for agent type %q", a.agentType)
	}
	// Without a prompt the agent would sit on an open session that never
	// produces anything; fail loudly instead of hanging the task.
	if strings.TrimSpace(req.Prompt) == "" && req.ResumeSessionID == "" {
		return adapters.StartResult{}, errors.New("acp: empty prompt: refusing to open an idle session")
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
			FS:       fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
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
		// The agent replays the conversation before answering session/load.
		// Those chunks are history the client already has (the hub serves it
		// through the history proxy), so they must not be mistaken for this
		// turn's output.
		a.loading.Store(true)
		raw, err := a.call(ctx, "session/load", sessionLoadParams{
			SessionID:  a.ResumeSessionID,
			Cwd:        workspace,
			MCPServers: servers,
		})
		a.loading.Store(false)
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

// SendMessage either answers a pending permission or elicitation request
// (when one is open) or starts a new turn with the user's text.
// PendingAsk reports the outstanding user-input request, if any. The node
// calls it after reconnecting: the ask may have been emitted while the link
// was down, and without it the session waits for a reply the hub never saw.
func (a *Adapter) PendingAsk() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingAsk, a.pendingAsk != ""
}

func (a *Adapter) SendMessage(ctx context.Context, sessionID string, message string) error {
	a.mu.Lock()
	if !a.started || a.sessionID != sessionID {
		a.mu.Unlock()
		return fmt.Errorf("acp: unknown session %q", sessionID)
	}
	perm := a.awaitingPerm
	elicit := a.awaitingElicit
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
	if elicit != nil {
		// A turn is paused on a form elicitation: this message is the reply.
		select {
		case elicit <- message:
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
	if a.terminals != nil {
		a.terminals.killAll()
	}

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

// clientCapabilities advertises exactly what this adapter serves:
// workspace-sandboxed fs read/write (fs.go), form-mode elicitation
// (elicitation.go), and host terminals (terminal.go). URL-mode elicitation is
// deliberately absent — claiming unimplemented capabilities would let an agent
// hang on a call that never answers, and the refusals in handleAgentRequest
// exist only as a safety net for agents that call unadvertised methods anyway.
//
// The empty struct matters: ACP advertises form support only when `form` is
// present and non-null, and `{}` is the required wire value.
type clientCapabilities struct {
	FS          fsCapabilities          `json:"fs"`
	Elicitation elicitationCapabilities `json:"elicitation"`
	Terminal    bool                    `json:"terminal"`
}

type elicitationCapabilities struct {
	Form struct{} `json:"form"`
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

	// The turn is only finished once its response arrives (handlePromptResult
	// clears the flag); an exit before that is a failure. Its activity flag
	// starts empty so a silent turn can be detected at the end.
	a.turnPending.Store(true)
	a.turnActivity.Store(false)

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
// thoughtTokens, plus cachedReadTokens/cachedWriteTokens when the provider
// exposes prompt caching). Fields are relayed verbatim; the agent's
// totalTokens is kept even though it may include thoughtTokens beyond
// input + output. ACP's inputTokens is the non-cached input portion, so cache
// reads/writes are separate input categories.
type usageReport struct {
	InputTokens       int64  `json:"inputTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	TotalTokens       int64  `json:"totalTokens"`
	ThoughtTokens     int64  `json:"thoughtTokens"`
	CachedReadTokens  *int64 `json:"cachedReadTokens"`
	CachedWriteTokens *int64 `json:"cachedWriteTokens"`
}

func (a *Adapter) handlePromptResult(ctx context.Context, sink adapters.EventSink, resp rpcResponse) {
	a.turnPending.Store(false)
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
			Model:             a.modelOrAgentType(),
			PromptTokens:      res.Usage.InputTokens,
			CompletionTokens:  res.Usage.OutputTokens,
			TotalTokens:       res.Usage.TotalTokens,
			CachedReadTokens:  res.Usage.CachedReadTokens,
			CachedWriteTokens: res.Usage.CachedWriteTokens,
		})
	}
	switch res.StopReason {
	case "end_turn", "":
		// A turn that reported usage did run the model even when it produced
		// no text (tool-only or resumed turns); only a turn with no output,
		// no activity and no usage is a failure.
		hadOutput := a.flushAssistantText(ctx, sink) || a.turnActivity.Load() || res.Usage != nil
		if !hadOutput {
			a.terminate(ctx, sink, errors.New("acp: agent finished the turn without producing any output"))
			return
		}
		a.finishTurn(ctx, sink)
	case "cancelled":
		a.flushAssistantText(ctx, sink)
		a.terminate(ctx, sink, nil)
	case "refusal", "max_tokens", "max_turn_requests":
		a.flushAssistantText(ctx, sink)
		a.terminate(ctx, sink, fmt.Errorf("acp: prompt stopped: %s", res.StopReason))
	default:
		a.flushAssistantText(ctx, sink)
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
// session/request_permission (the pause-and-pick flow), form-mode
// elicitation/create (elicitation.go), workspace-sandboxed fs/*, and the
// terminal/* family (terminal.go). Unadvertised elicitation modes are refused
// with invalid-params; unknown methods get method-not-found, so a misbehaving
// agent cannot stall the session.
func (a *Adapter) handleAgentRequest(ctx context.Context, sessionID string, sink adapters.EventSink, msg inbound) {
	switch msg.Method {
	case "session/request_permission":
		a.handlePermissionRequest(ctx, sessionID, sink, msg)
	case "elicitation/create":
		a.handleElicitationRequest(ctx, sessionID, sink, msg)
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		a.handleTerminalRequest(ctx, msg)
	case "fs/read_text_file", "fs/write_text_file":
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
	SessionUpdate string `json:"sessionUpdate"`
	MessageID     string `json:"messageId"`
	ToolCallID    string `json:"toolCallId"`
	// Content is kept raw: a text chunk carries one content block, while a
	// tool_call's content is an array of blocks. Decoding into a struct made
	// the whole notification fail for array payloads, silently dropping tool
	// calls that reported content.
	Content  json.RawMessage `json:"content"`
	Title    string          `json:"title"`
	Kind     string          `json:"kind"`
	Status   string          `json:"status"`
	RawInput json.RawMessage `json:"rawInput"`
	Entries  []planEntry     `json:"entries"`
}

// contentText returns the text of a chunk payload. It accepts either a single
// content block (agent/user/thought chunks) or an array (tool call content);
// non-text blocks yield "".
func (u sessionUpdate) contentText() string {
	var block contentBlock
	if json.Unmarshal(u.Content, &block) == nil {
		return block.Text
	}
	var blocks []contentBlock
	if json.Unmarshal(u.Content, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" || block.Text != "" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
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
	// The title command turn is adapter-internal: only the title it reports
	// may reach the event stream (omp also prints "Session renamed to ..." as
	// an agent message, which is not part of the task's answer).
	if a.titleTurn.Load() && p.Update.SessionUpdate != "session_info_update" {
		return
	}
	// Session history replayed by session/load during a live resume is not
	// live output. The history proxy collects it via replayAsm instead.
	if a.replayAsm == nil && a.loading.Load() {
		return
	}

	switch p.Update.SessionUpdate {
	case "agent_message_chunk":
		if text := p.Update.contentText(); text != "" {
			a.turnActivity.Store(true)
			if a.replayAsm != nil {
				a.replayAsm.chunk(p.Update.MessageID, protocol.RoleAssistant, text)
				return
			}
			a.appendText(text)
			if sink != nil {
				sink.TextDelta(ctx, sessionID, text)
			}
		}
	case "user_message_chunk":
		// Only a session/load replay carries user turns (live agents do not
		// echo prompts), so this path is replay-only by construction.
		if a.replayAsm != nil {
			if text := p.Update.contentText(); text != "" {
				a.replayAsm.chunk(p.Update.MessageID, protocol.RoleUser, text)
			}
		}
	case "agent_thought_chunk":
		// Reasoning is not assistant text: live turns stream it on the
		// ThoughtSink (never mixed into the answer), and a session/load replay
		// rebuilds it as a `thinking` history message — the agent's own store
		// is the durable copy.
		if text := p.Update.contentText(); text != "" {
			a.turnActivity.Store(true)
			if a.replayAsm != nil {
				a.replayAsm.chunk(p.Update.MessageID, protocol.RoleThinking, text)
				return
			}
			if ts, ok := sink.(adapters.ThoughtSink); ok {
				ts.ThoughtDelta(ctx, sessionID, text)
			}
		}
	case "session_info_update":
		// Agents name their session (codex-acp generates one after the turn;
		// omp answers the `/rename` command with one). The hub backfills a
		// provisional task title from it; a title the operator chose is never
		// touched. A replay run must not mutate the task, so it is skipped
		// there.
		if title := strings.TrimSpace(p.Update.Title); title != "" && a.replayAsm == nil {
			if !a.titleDone.Swap(true) {
				select {
				case a.titleCh <- struct{}{}:
				default:
				}
			}
			if ts, ok := sink.(adapters.TitleSink); ok {
				ts.Title(ctx, sessionID, title)
			}
		}
	case "plan":
		if todos := parsePlan(p.Update.Entries); len(todos) > 0 && sink != nil {
			a.turnActivity.Store(true)
			sink.TodoUpdate(ctx, sessionID, todos)
		}
	case "tool_call", "tool_call_update":
		a.turnActivity.Store(true)
		call := adapters.ToolCall{
			ID:     p.Update.ToolCallID,
			Title:  strings.TrimSpace(p.Update.Title),
			Kind:   p.Update.Kind,
			Status: p.Update.Status,
		}
		// session/load replays tool calls too; the assembler turns them into
		// ordered `tool` history rows for finished sessions.
		if a.replayAsm != nil {
			a.replayAsm.tool(call)
			return
		}
		if todos := parseTodoToolCall(p.Update.Title, p.Update.RawInput); len(todos) > 0 && sink != nil {
			sink.TodoUpdate(ctx, sessionID, todos)
		}
		// Close the text segment before the tool row: any text produced before
		// this call belongs above it in the transcript, and text after it will
		// start a fresh segment.
		a.flushAssistantText(ctx, sink)
		// A tool call without an id cannot be updated in place; showing one row
		// per lifecycle frame would be noise, so skip it.
		if call.ID == "" || sink == nil {
			return
		}
		if ts, ok := sink.(adapters.ToolCallSink); ok {
			ts.ToolCall(ctx, sessionID, call)
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
	if a.turnPending.Load() {
		a.terminate(ctx, sink, errors.New("acp: agent exited before finishing the turn"))
		return
	}
	a.terminate(ctx, sink, nil)
}

// finishSession reports the terminal outcome exactly once (Done/Error) and
// sets the terminal status. Done and Error carry the final state themselves
// (see EventSink), so no separate StateChange is emitted. It never reaps the
// process, so a caller can keep the agent alive briefly to receive a trailing
// session title.
func (a *Adapter) finishSession(ctx context.Context, sink adapters.EventSink, err error) {
	if a.terminals != nil {
		a.terminals.killAll()
	}
	if !a.committed.Load() {
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
	})
}

// terminate reports the terminal outcome exactly once and reaps the process.
// It is a no-op before Start commits.
func (a *Adapter) terminate(ctx context.Context, sink adapters.EventSink, err error) {
	a.finishSession(ctx, sink, err)
	a.killProc()
}

// finishTurn ends a successful, completed turn: the session is reported done
// first (the task must not wait on title generation), then the process stays
// alive just long enough to collect the agent's own session title, and is
// reaped.
func (a *Adapter) finishTurn(ctx context.Context, sink adapters.EventSink) {
	a.finishSession(ctx, sink, nil)
	a.collectTitle(ctx)
	a.killProc()
}

// collectTitle obtains the agent's title for a session the hub still treats as
// provisionally named. Two sources exist: a launcher that cannot name itself
// is asked with its own title command (omp's `/rename`); a launcher that names
// the thread on its own after answering the turn (codex-acp) gets a grace
// window to deliver the update. Either way the wait is bounded and the task has
// already been reported done.
func (a *Adapter) collectTitle(ctx context.Context) {
	// At most once per adapter: follow-up turns must not re-trigger it.
	if !a.titleChecked.CompareAndSwap(false, true) {
		return
	}
	if !a.wantTitle.CompareAndSwap(true, false) {
		return
	}
	if a.cfg.titlePrompt != "" {
		// A title reported during the turn makes the request unnecessary.
		if !a.titleDone.Load() {
			a.requestAgentTitle(ctx)
		}
		return
	}
	// The grace is unconditional: codex-acp publishes a prompt-derived
	// fallback title *before* answering the turn and renames the thread to its
	// generated title afterwards. That second update must still be read, so an
	// earlier title must not shorten the window.
	if grace := a.cfg.titleGrace; grace > 0 {
		a.titleTurn.Store(true)
		defer a.titleTurn.Store(false)
		a.linger(ctx, grace)
	}
}

// linger keeps the session's read loop alive for d (or until the context is
// cancelled) so trailing title updates are relayed before the process is
// reaped. The task has already been reported done.
func (a *Adapter) linger(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// requestAgentTitle sends the launcher's title command as one internal turn
// and waits for the title it reports. Its output is suppressed by
// handleNotification; only the session_info_update title is relayed.
func (a *Adapter) requestAgentTitle(ctx context.Context) {
	prompt := strings.TrimSpace(a.cfg.titlePrompt)
	if prompt == "" {
		return
	}
	a.mu.Lock()
	external := a.externalID
	proc := a.proc
	a.mu.Unlock()
	if proc == nil || external == "" {
		return
	}

	a.titleTurn.Store(true)
	defer a.titleTurn.Store(false)

	// A cancelled per-message ctx must not abort the request the moment the
	// turn ends; node shutdown still stops it through proc.done/cancelCh.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), titleRequestTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.call(callCtx, "session/prompt", sessionPromptParams{
			SessionID: external,
			Prompt:    []contentBlock{{Type: "text", Text: prompt}},
		})
	}()

	select {
	case <-a.titleCh:
		// The title arrived; hold the output suppression until the command's
		// own response (or a short grace) so trailing command chatter —
		// omp streams "Session renamed to ..." — cannot leak into the task.
		select {
		case <-done:
		case <-time.After(titleResponseGrace):
		case <-callCtx.Done():
		}
	case <-done:
		// Agents emit session_info_update just before answering the title
		// command; a short grace covers one emitted right after it.
		a.awaitTitle(callCtx, titleResponseGrace)
	case <-callCtx.Done():
	}
}

// awaitTitle waits up to d for a title update, returning whether one arrived.
func (a *Adapter) awaitTitle(ctx context.Context, d time.Duration) bool {
	if a.titleDone.Load() {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-a.titleCh:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
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

// flushAssistantText emits the accumulated agent text as one complete
// assistant message and resets the accumulator. It is called when a tool call
// starts (so the transcript reads text → tool → next text instead of putting
// the whole turn's text after the tool) and at the end of the turn. It reports
// whether anything was sent.
func (a *Adapter) flushAssistantText(ctx context.Context, sink adapters.EventSink) bool {
	if sink == nil {
		return false
	}
	a.accMu.Lock()
	content := a.acc.String()
	a.acc.Reset()
	a.accMu.Unlock()
	if strings.TrimSpace(content) == "" {
		return false
	}
	sessionID := a.currentSessionID()
	sink.Message(ctx, sessionID, protocol.Message{
		ID:        fmt.Sprintf("%s-msg-%d", sessionID, a.msgSeq.Add(1)),
		SessionID: sessionID,
		Role:      protocol.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	})
	return true
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

// autoAllow returns the workspace's permission auto-allow range and the
// workspace path it is relative to.
func (a *Adapter) autoAllow() (protocol.AutoAllowScope, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autoAllowScope, a.workspace
}

func (a *Adapter) setStatus(status protocol.SessionStatus) {
	a.mu.Lock()
	a.status = status
	a.mu.Unlock()
}
