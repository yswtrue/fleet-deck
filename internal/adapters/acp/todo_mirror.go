package acp

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"fleetdeck/internal/protocol"
)

// ---- todo mirror -----------------------------------------------------------

// todoTitleRe matches the tool titles that carry a todo list. The `\b` guard
// keeps e.g. "planner" from matching "plan".
var todoTitleRe = regexp.MustCompile(`(?i)^(todowrite|todo_write|update_plan|plan)\b`)

type todoItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

// parseTodoToolCall mirrors a `todo_write`-style tool call into todos. It
// accepts a rawInput that is either a bare array or an object carrying a
// `todos`/`items`/`plan` array. Returns nil when the title does not match or
// nothing parses.
func parseTodoToolCall(title string, rawInput json.RawMessage) []protocol.Todo {
	if !todoTitleRe.MatchString(strings.TrimSpace(title)) || len(rawInput) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(rawInput)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}

	if trimmed[0] == '[' {
		var items []todoItem
		if json.Unmarshal(trimmed, &items) != nil {
			return nil
		}
		return toTodos(items)
	}

	var wrapper struct {
		Todos []todoItem `json:"todos"`
		Items []todoItem `json:"items"`
		Plan  []todoItem `json:"plan"`
	}
	if json.Unmarshal(trimmed, &wrapper) != nil {
		return nil
	}
	switch {
	case len(wrapper.Todos) > 0:
		return toTodos(wrapper.Todos)
	case len(wrapper.Items) > 0:
		return toTodos(wrapper.Items)
	case len(wrapper.Plan) > 0:
		return toTodos(wrapper.Plan)
	default:
		return nil
	}
}

// parsePlan mirrors an ACP native `plan` update's entries into todos.
func parsePlan(entries []planEntry) []protocol.Todo {
	if len(entries) == 0 {
		return nil
	}
	items := make([]todoItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, todoItem{Content: e.Content, Status: e.Status})
	}
	return toTodos(items)
}

func toTodos(items []todoItem) []protocol.Todo {
	out := make([]protocol.Todo, 0, len(items))
	for i, it := range items {
		title := firstNonEmpty(it.Title, it.Content, it.Text)
		if title == "" {
			continue
		}
		id := it.ID
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		out = append(out, protocol.Todo{
			ID:     id,
			Title:  title,
			Status: mapTodoStatus(it.Status),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mapTodoStatus normalizes the status vocabulary across agents. ACP uses
// `completed`; pi/claude tool calls use `completed` too; some agents emit
// `done`. Unknown values fall back to pending.
func mapTodoStatus(status string) protocol.SubtaskStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "complete", "success", "resolved":
		return protocol.SubtaskDone
	case "in_progress", "in-progress", "inprogress", "active", "running", "doing", "started":
		return protocol.SubtaskInProgress
	default:
		return protocol.SubtaskPending
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
