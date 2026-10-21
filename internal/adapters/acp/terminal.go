package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// ---- client terminals ------------------------------------------------------
//
// ACP terminal methods let the agent run commands in the client's environment
// and poll their output. The adapter advertises clientCapabilities.terminal and
// serves the five client methods:
//
//	terminal/create, terminal/output, terminal/wait_for_exit,
//	terminal/kill, terminal/release
//
// Threat model: commands run as the node user with the node's environment plus
// the requested overrides. `cwd` is confined to the session workspace (the same
// boundary as fs/*), output is capped (default 1 MiB, max 8 MiB), and every
// terminal is killed when the session ends. This is not an OS-level sandbox: a
// command can still reach outside the workspace by absolute path, exactly like
// the agent's own shell.

const (
	defaultTerminalOutputBytes = 1 << 20
	maxTerminalOutputBytes     = 8 << 20
)

type terminalEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type terminalCreateParams struct {
	SessionID       string           `json:"sessionId"`
	Command         string           `json:"command"`
	Args            []string         `json:"args,omitempty"`
	Env             []terminalEnvVar `json:"env,omitempty"`
	Cwd             string           `json:"cwd,omitempty"`
	OutputByteLimit *int64           `json:"outputByteLimit,omitempty"`
}

type terminalRefParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type terminalExit struct {
	ExitCode *int    `json:"exitCode"`
	Signal   *string `json:"signal"`
}

type terminalSession struct {
	id   string
	cmd  *exec.Cmd
	buf  *boundedOutput
	done chan struct{}

	mu     sync.Mutex
	exit   *terminalExit
	killed bool
}

type terminalManager struct {
	mu      sync.Mutex
	next    atomic.Int64
	created atomic.Int64
	terms   map[string]*terminalSession
}

func newTerminalManager() *terminalManager {
	return &terminalManager{terms: make(map[string]*terminalSession)}
}

func (m *terminalManager) create(p terminalCreateParams, workspace string) (*terminalSession, error) {
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return nil, errors.New("terminal command is required")
	}
	cwd := strings.TrimSpace(p.Cwd)
	if cwd == "" {
		cwd = workspace
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("terminal cwd must be absolute")
	}
	target, err := resolveInWorkspace(workspace, cwd)
	if err != nil {
		return nil, err
	}

	limit := int64(defaultTerminalOutputBytes)
	if p.OutputByteLimit != nil {
		limit = *p.OutputByteLimit
		if limit < 0 {
			return nil, errors.New("outputByteLimit must not be negative")
		}
		if limit > maxTerminalOutputBytes {
			limit = maxTerminalOutputBytes
		}
	}

	env, err := terminalEnv(p.Env)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(command, p.Args...)
	cmd.Dir = target
	cmd.Env = env
	buf := newBoundedOutput(int(limit))
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	t := &terminalSession{
		id:   fmt.Sprintf("term_%d", m.next.Add(1)),
		cmd:  cmd,
		buf:  buf,
		done: make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		t.mu.Lock()
		t.exit = terminalExitFromState(cmd.ProcessState, t.killed)
		t.mu.Unlock()
		close(t.done)
	}()

	m.mu.Lock()
	m.terms[t.id] = t
	m.mu.Unlock()
	m.created.Add(1)
	return t, nil
}

// count reports how many terminals this adapter created (released or not).
func (m *terminalManager) count() int64 {
	return m.created.Load()
}

func (m *terminalManager) get(id string) (*terminalSession, error) {
	m.mu.Lock()
	t := m.terms[id]
	m.mu.Unlock()
	if t == nil {
		return nil, fmt.Errorf("unknown terminal %q", id)
	}
	return t, nil
}

func (m *terminalManager) release(id string) error {
	m.mu.Lock()
	t := m.terms[id]
	delete(m.terms, id)
	m.mu.Unlock()
	if t == nil {
		return fmt.Errorf("unknown terminal %q", id)
	}
	t.kill()
	return nil
}

func (m *terminalManager) killAll() {
	m.mu.Lock()
	terms := make([]*terminalSession, 0, len(m.terms))
	for id, t := range m.terms {
		terms = append(terms, t)
		delete(m.terms, id)
	}
	m.mu.Unlock()
	for _, t := range terms {
		t.kill()
	}
}

func (t *terminalSession) output() (string, bool, *terminalExit) {
	t.mu.Lock()
	exit := t.exit
	t.mu.Unlock()
	output, truncated := t.buf.snapshot()
	return output, truncated, exit
}

func (t *terminalSession) kill() {
	t.mu.Lock()
	t.killed = true
	proc := t.cmd.Process
	t.mu.Unlock()
	if proc != nil {
		_ = proc.Kill()
	}
}

func terminalExitFromState(state *os.ProcessState, killed bool) *terminalExit {
	if state == nil {
		return nil
	}
	code := state.ExitCode()
	if code >= 0 {
		return &terminalExit{ExitCode: &code}
	}
	if killed {
		signal := "killed"
		return &terminalExit{Signal: &signal}
	}
	signal := "unknown"
	return &terminalExit{Signal: &signal}
}

// terminalEnv starts from the node process environment and applies the
// requested overrides once, so duplicate names have a deterministic winner.
func terminalEnv(overrides []terminalEnvVar) ([]string, error) {
	env := make(map[string]string, len(os.Environ())+len(overrides))
	order := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := env[name]; !exists {
			order = append(order, name)
		}
		env[name] = value
	}
	for _, entry := range overrides {
		if entry.Name == "" || strings.ContainsAny(entry.Name, "=\x00") {
			return nil, fmt.Errorf("invalid terminal env name %q", entry.Name)
		}
		if strings.ContainsRune(entry.Value, 0) {
			return nil, fmt.Errorf("terminal env %q contains NUL", entry.Name)
		}
		if _, exists := env[entry.Name]; !exists {
			order = append(order, entry.Name)
		}
		env[entry.Name] = entry.Value
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, name+"="+env[name])
	}
	return out, nil
}

// boundedOutput retains the most recent limit bytes of command output.
// Truncation drops from the beginning (the ACP contract) and trims a partial
// UTF-8 rune so the string is always valid.
type boundedOutput struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newBoundedOutput(limit int) *boundedOutput {
	if limit < 0 {
		limit = 0
	}
	return &boundedOutput{limit: limit}
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit == 0 {
		b.truncated = b.truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) >= b.limit {
		if len(p) > b.limit || len(b.data) > 0 {
			b.truncated = true
		}
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > b.limit {
			drop := len(b.data) - b.limit
			copy(b.data, b.data[drop:])
			b.data = b.data[:b.limit]
			b.truncated = true
		}
	}
	// Drop leading continuation bytes left by a mid-rune cut.
	for len(b.data) > 0 && b.data[0]&0xc0 == 0x80 {
		b.data = b.data[1:]
	}
	return len(p), nil
}

func (b *boundedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data := b.data
	// A trailing partial rune can only come from a write boundary; removing it
	// keeps the JSON string valid.
	for len(data) > 0 {
		r, size := utf8.DecodeLastRune(data)
		if r != utf8.RuneError || size != 1 {
			break
		}
		data = data[:len(data)-1]
	}
	return string(data), b.truncated
}

// ---- request handlers ------------------------------------------------------

func (a *Adapter) handleTerminalRequest(ctx context.Context, msg inbound) {
	switch msg.Method {
	case "terminal/create":
		a.handleTerminalCreate(msg)
	case "terminal/output":
		a.handleTerminalOutput(msg)
	case "terminal/wait_for_exit":
		a.handleTerminalWait(ctx, msg)
	case "terminal/kill":
		a.handleTerminalKill(msg)
	case "terminal/release":
		a.handleTerminalRelease(msg)
	}
}

func (a *Adapter) handleTerminalCreate(msg inbound) {
	var p terminalCreateParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		a.respond(msg.ID, nil, &rpcError{Code: -32602, Message: "invalid terminal/create params"})
		return
	}
	t, err := a.terminals.create(p, a.currentWorkspace())
	if err != nil {
		a.respond(msg.ID, nil, &rpcError{Code: -32603, Message: "terminal/create: " + err.Error()})
		return
	}
	a.respond(msg.ID, map[string]any{"terminalId": t.id}, nil)
}

func (a *Adapter) handleTerminalOutput(msg inbound) {
	p, ok := a.terminalRef(msg.ID, msg.Params)
	if !ok {
		return
	}
	t, err := a.terminals.get(p.TerminalID)
	if err != nil {
		a.respondTerminalUnknown(msg.ID, err)
		return
	}
	output, truncated, exit := t.output()
	result := map[string]any{"output": output, "truncated": truncated}
	if exit != nil {
		result["exitStatus"] = exit
	}
	a.respond(msg.ID, result, nil)
}

func (a *Adapter) handleTerminalWait(ctx context.Context, msg inbound) {
	p, ok := a.terminalRef(msg.ID, msg.Params)
	if !ok {
		return
	}
	t, err := a.terminals.get(p.TerminalID)
	if err != nil {
		a.respondTerminalUnknown(msg.ID, err)
		return
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		a.respond(msg.ID, nil, &rpcError{Code: -32603, Message: "terminal/wait_for_exit cancelled"})
		return
	}
	t.mu.Lock()
	exit := t.exit
	t.mu.Unlock()
	if exit == nil {
		exit = &terminalExit{}
	}
	a.respond(msg.ID, exit, nil)
}

func (a *Adapter) handleTerminalKill(msg inbound) {
	p, ok := a.terminalRef(msg.ID, msg.Params)
	if !ok {
		return
	}
	t, err := a.terminals.get(p.TerminalID)
	if err != nil {
		a.respondTerminalUnknown(msg.ID, err)
		return
	}
	t.kill()
	a.respond(msg.ID, map[string]any{}, nil)
}

func (a *Adapter) handleTerminalRelease(msg inbound) {
	p, ok := a.terminalRef(msg.ID, msg.Params)
	if !ok {
		return
	}
	if err := a.terminals.release(p.TerminalID); err != nil {
		a.respondTerminalUnknown(msg.ID, err)
		return
	}
	a.respond(msg.ID, map[string]any{}, nil)
}

func (a *Adapter) terminalRef(id json.RawMessage, params json.RawMessage) (terminalRefParams, bool) {
	var p terminalRefParams
	if err := json.Unmarshal(params, &p); err != nil || p.TerminalID == "" {
		a.respond(id, nil, &rpcError{Code: -32602, Message: "invalid terminal params"})
		return p, false
	}
	return p, true
}

func (a *Adapter) respondTerminalUnknown(id json.RawMessage, err error) {
	a.respond(id, nil, &rpcError{Code: -32602, Message: err.Error()})
}
