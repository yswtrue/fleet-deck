package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// process wraps one ACP agent subprocess. ACP speaks JSON-RPC 2.0 over
// newline-delimited JSON on stdio: exactly one JSON object per line on each
// direction, no embedded newlines. Reads are line-delimited; writes are
// serialized so concurrent callers never interleave bytes on stdin.
//
// stderr is intentionally left unconnected: exec routes it to the null device.
// The ACP spec reserves stderr for agent logs (never protocol frames), so
// parsing it is pointless, and a pipe nobody drains would eventually block the
// child. Agent log output is therefore not captured; wire cmd.Stderr to a
// logger if debugging ever needs it.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex

	// done is closed once cmd.Wait returns; waitErr holds its result.
	done    chan struct{}
	waitErr error
}

// startProcess resolves command on PATH, spawns it with the given args/env in
// dir, and wires up NDJSON stdio. The binary is resolved eagerly so a missing
// agent surfaces as exec.ErrNotFound instead of a cryptic exec failure.
func startProcess(ctx context.Context, command string, args, env []string, dir string) (*process, error) {
	if _, err := exec.LookPath(command); err != nil {
		return nil, err // *exec.Error wrapping exec.ErrNotFound
	}

	cmd := exec.CommandContext(ctx, command, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: start %q: %w", command, err)
	}

	p := &process{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// writeLine marshals v to JSON and writes it as one line. A trailing newline is
// the NDJSON frame delimiter.
func (p *process) writeLine(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(raw)
	return err
}

// readLine returns the next raw line (including any trailing newline). A final
// unterminated line is returned with a nil error so it is not lost; the next
// call reports io.EOF.
func (p *process) readLine() ([]byte, error) {
	line, err := p.stdout.ReadBytes('\n')
	if len(line) > 0 {
		return line, nil
	}
	return nil, err
}

// kill forcibly terminates the child. Safe to call multiple times and after
// the process has already exited.
func (p *process) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// closeStdin signals EOF to the child so agents that exit on stdin close shut
// down gracefully.
func (p *process) closeStdin() {
	_ = p.stdin.Close()
}
