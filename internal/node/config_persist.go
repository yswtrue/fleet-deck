package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rewriteConfig applies mutate to the config file at path while leaving every
// byte outside the edited keys (comments, ordering, spacing) untouched, and
// replaces the file atomically. A missing file is an error: the daemon never
// invents a config the operator did not point it at.
func rewriteConfig(path string, mutate func(raw []byte) ([]byte, bool)) error {
	if path == "" {
		return errors.New("no config file to update (start with --config <file>)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, changed := mutate(raw)
	if !changed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node-config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// persistHubURL writes hubURL into the config file after the hub accepted a
// --hub-url registration, so later starts need only --config.
func persistHubURL(path, hubURL string) error {
	return rewriteConfig(path, func(raw []byte) ([]byte, bool) {
		return setTopLevel(raw, "hub_url", "hub_url: "+hubURL)
	})
}

// PersistAgents rewrites the agent binding (`agent_types` + `default_adapter`)
// in the config file. Callers validate agentTypes against the adapter registry
// first; an empty slice writes `agent_types: []`.
func PersistAgents(path string, agentTypes []string, defaultAdapter string) error {
	return rewriteConfig(path, func(raw []byte) ([]byte, bool) {
		return setAgentTypes(raw, agentTypes, defaultAdapter)
	})
}

// setAgentTypes rewrites the agent_types block and, when non-empty, the
// default_adapter scalar.
func setAgentTypes(raw []byte, agentTypes []string, defaultAdapter string) ([]byte, bool) {
	block := "agent_types: []"
	if len(agentTypes) > 0 {
		var b strings.Builder
		b.WriteString("agent_types:")
		for _, a := range agentTypes {
			b.WriteString("\n  - ")
			b.WriteString(a)
		}
		block = b.String()
	}
	out, changed := setTopLevel(raw, "agent_types", block)
	if defaultAdapter == "" {
		return out, changed
	}
	out2, changed2 := setTopLevel(out, "default_adapter", "default_adapter: "+defaultAdapter)
	return out2, changed || changed2
}

// setTopLevel replaces the top-level YAML key `key` with block — the key line
// plus any value lines, without a trailing newline — and reports whether the
// content changed. A trailing comment on a scalar key survives, and `key` is
// appended when absent. Nested keys and comments are never matched.
func setTopLevel(raw []byte, key, block string) ([]byte, bool) {
	text := string(raw)
	lines := strings.Split(text, "\n")

	start := -1
	for i, line := range lines {
		if isTopLevelKey(line, key) {
			start = i
			break
		}
	}
	if start < 0 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + block + "\n"), true
	}

	end := start + 1
	if !hasInlineValue(lines[start], key) {
		for end < len(lines) && isBlockContinuation(lines, end) {
			end++
		}
	}

	replacement := block
	if !strings.Contains(block, "\n") {
		replacement += inlineComment(lines[start], key)
	}
	newLines := make([]string, 0, len(lines)-(end-start)+1)
	newLines = append(newLines, lines[:start]...)
	newLines = append(newLines, strings.Split(replacement, "\n")...)
	newLines = append(newLines, lines[end:]...)
	out := strings.Join(newLines, "\n")
	if out == text {
		return raw, false
	}
	return []byte(out), true
}

// isTopLevelKey reports whether line starts a top-level `key:` entry (so a
// comment, an indented key, or a longer key such as `hub_urls` never matches).
func isTopLevelKey(line, key string) bool {
	if !strings.HasPrefix(line, key) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(line[len(key):], " \t"), ":")
}

// splitKeyValue returns the text after the key's colon.
func splitKeyValue(line, key string) string {
	return line[len(key)+strings.Index(line[len(key):], ":")+1:]
}

func hasInlineValue(line, key string) bool {
	value := strings.TrimSpace(splitKeyValue(line, key))
	return value != "" && !strings.HasPrefix(value, "#")
}

// inlineComment returns the trailing ` # comment` of a key's line, if any.
func inlineComment(line, key string) string {
	value := splitKeyValue(line, key)
	if i := strings.Index(value, " #"); i >= 0 {
		return value[i:]
	}
	return ""
}

// isBlockContinuation reports whether lines[i] still belongs to the value of
// the key above it: an indented line, a block-sequence item, or a blank line
// that is followed by one of those.
func isBlockContinuation(lines []string, i int) bool {
	line := strings.TrimRight(lines[i], "\r")
	if strings.TrimSpace(line) == "" {
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimRight(lines[j], "\r")
			if strings.TrimSpace(next) == "" {
				continue
			}
			return next[0] == ' ' || next[0] == '\t' || strings.HasPrefix(next, "- ")
		}
		return false
	}
	return line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "- ")
}
