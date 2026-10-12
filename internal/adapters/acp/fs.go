package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ---- fs/* (client methods the agent may call) --------------------------------
//
// The adapter serves fs/read_text_file and fs/write_text_file against the
// session workspace and advertises exactly that in initialize. terminal/* and
// elicitation are deliberately NOT advertised and are refused with
// method-not-found: claiming unimplemented capabilities would let an agent
// hang on a call that never answers.

type fsReadParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`  // 1-based start line; 0 = from the top
	Limit     int    `json:"limit,omitempty"` // max lines; 0 = all
}

type fsWriteParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// resolveInWorkspace maps a client-supplied path onto the session workspace.
// Relative paths are workspace-relative; absolute paths must already live
// inside the workspace. Symlinks are resolved before the containment check, so
// neither a `..` escape nor a link pointing outside the workspace can be used
// to read or write arbitrary files.
func resolveInWorkspace(workspace, p string) (string, error) {
	if workspace == "" {
		return "", errors.New("acp: no workspace bound to this session")
	}
	if strings.TrimSpace(p) == "" {
		return "", errors.New("acp: empty path")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("acp: resolve workspace: %w", err)
	}

	cand := p
	if !filepath.IsAbs(cand) {
		cand = filepath.Join(workspace, cand)
	}
	cand = filepath.Clean(cand)

	var resolved string
	if target, err := filepath.EvalSymlinks(cand); err == nil {
		resolved = target
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("acp: resolve %q: %w", p, err)
	} else {
		// New leaf (a file about to be written): resolve the deepest existing
		// ancestor so a symlinked parent cannot escape either.
		parent, err := filepath.EvalSymlinks(filepath.Dir(cand))
		if err != nil {
			return "", fmt.Errorf("acp: resolve %q: %w", p, err)
		}
		resolved = filepath.Join(parent, filepath.Base(cand))
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("acp: path %q escapes the workspace", p)
	}
	return resolved, nil
}

// handleFSRequest answers one fs/* request. Errors are reported as JSON-RPC
// errors so the agent sees a refusal instead of hanging.
func (a *Adapter) handleFSRequest(msg inbound) {
	workspace := a.currentWorkspace()

	switch msg.Method {
	case "fs/read_text_file":
		var p fsReadParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32602, Message: "invalid fs/read_text_file params"})
			return
		}
		target, err := resolveInWorkspace(workspace, p.Path)
		if err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32602, Message: err.Error()})
			return
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32603, Message: "read failed: " + err.Error()})
			return
		}
		a.respond(msg.ID, map[string]any{"content": sliceLines(string(raw), p.Line, p.Limit)}, nil)

	case "fs/write_text_file":
		var p fsWriteParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32602, Message: "invalid fs/write_text_file params"})
			return
		}
		target, err := resolveInWorkspace(workspace, p.Path)
		if err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32602, Message: err.Error()})
			return
		}
		if err := os.WriteFile(target, []byte(p.Content), 0o644); err != nil {
			a.respond(msg.ID, nil, &rpcError{Code: -32603, Message: "write failed: " + err.Error()})
			return
		}
		a.respond(msg.ID, map[string]any{}, nil)
	}
}

// sliceLines applies the optional 1-based line window of fs/read_text_file.
func sliceLines(content string, line, limit int) string {
	if line <= 0 && limit <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line > 1 {
		start = line - 1
	}
	if start > len(lines) {
		return ""
	}
	lines = lines[start:]
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n")
}
