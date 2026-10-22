package acp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// ---- permission flow -------------------------------------------------------

type permissionParams struct {
	SessionID string                `json:"sessionId"`
	ToolCall  toolCallRef           `json:"toolCall"`
	Options   []permissionOptionRaw `json:"options"`
}

type toolCallRef struct {
	Title string `json:"title"`
}

type permissionOptionRaw struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// permissionOption is the web-facing shape embedded in the request prompt.
type permissionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// permissionReplyRe unwraps the user's reply to a permission request.
var permissionReplyRe = regexp.MustCompile(`\[permission_response\]\s*option_id="([^"]*)"`)

func (a *Adapter) handlePermissionRequest(ctx context.Context, sessionID string, sink adapters.EventSink, msg inbound) {
	var p permissionParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		a.respond(msg.ID, cancelledPermissionOutcome(), nil)
		return
	}

	question := strings.TrimSpace(p.ToolCall.Title)
	if question == "" {
		question = "Permission requested"
	}
	options := make([]permissionOption, 0, len(p.Options))
	for _, o := range p.Options {
		options = append(options, permissionOption{ID: o.OptionID, Label: o.Name})
	}
	raw, _ := json.Marshal(options)
	prompt := question + "\n\n[permission_request] options=" + string(raw)

	replyCh := make(chan string, 1)
	a.mu.Lock()
	a.awaitingPerm = replyCh
	a.pendingAsk = prompt
	a.status = protocol.SessionAwaitingUser
	proc := a.proc
	a.mu.Unlock()
	var procDone <-chan struct{}
	if proc != nil {
		procDone = proc.done
	}

	if sink != nil {
		sink.StateChange(ctx, sessionID, protocol.SessionAwaitingUser)
		sink.UserInputRequested(ctx, sessionID, prompt)
	}

	var reply string
	cancelled := false
	exited := false
	select {
	case reply = <-replyCh:
	case <-a.cancelCh:
		cancelled = true
	case <-procDone:
		// Agent exited while the picker was open.
		cancelled = true
		exited = true
	case <-ctx.Done():
		cancelled = true
	}

	a.mu.Lock()
	a.awaitingPerm = nil
	a.pendingAsk = ""
	if !exited {
		a.status = protocol.SessionRunning
	}
	a.mu.Unlock()

	if exited {
		return
	}
	if cancelled {
		a.respond(msg.ID, cancelledPermissionOutcome(), nil)
		return
	}

	optionID := parsePermissionReply(reply)
	if !knownOptionID(p.Options, optionID) {
		// Free-text replies may carry an arbitrary marker payload. Only an id
		// the agent actually offered may be forwarded; anything else falls
		// back to the safest option, and a reply without a usable marker logs
		// loudly.
		if optionID != "" {
			a.log.Warn("acp: permission reply names an unknown option; defaulting to deny", "option_id", optionID)
		} else {
			a.log.Warn("acp: could not parse permission reply; defaulting to deny", "reply", reply)
		}
		optionID = denyOptionID(p.Options)
	}
	if optionID == "" {
		a.respond(msg.ID, cancelledPermissionOutcome(), nil)
	} else {
		a.respond(msg.ID, selectedPermissionOutcome(optionID), nil)
	}

	if sink != nil {
		sink.StateChange(ctx, sessionID, protocol.SessionRunning)
	}
}

// parsePermissionReply extracts the option id from a permission reply, or ""
// when the reply does not carry one.
func parsePermissionReply(reply string) string {
	m := permissionReplyRe.FindStringSubmatch(reply)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// knownOptionID reports whether id names one of the request's options. The
// empty id never matches: it cannot name an option.
func knownOptionID(options []permissionOptionRaw, id string) bool {
	if id == "" {
		return false
	}
	for _, o := range options {
		if o.OptionID == id {
			return true
		}
	}
	return false
}

// denyOptionID picks the safest option to fall back to: an explicit reject
// kind if present, else a deny-labelled option. It returns "" when neither
// exists — the caller then cancels — because an arbitrary last option could be
// an allow one, and permission requests are never auto-approved. Options with
// an empty id can never be selected: the agent would not recognize them.
func denyOptionID(options []permissionOptionRaw) string {
	for _, o := range options {
		if o.OptionID == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(o.Kind), "reject") {
			return o.OptionID
		}
	}
	for _, o := range options {
		if o.OptionID == "" {
			continue
		}
		if strings.Contains(strings.ToLower(o.Name), "deny") {
			return o.OptionID
		}
	}
	return ""
}

func selectedPermissionOutcome(optionID string) map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
}

func cancelledPermissionOutcome() map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

func (a *Adapter) respond(id json.RawMessage, result any, rpcErr *rpcError) {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return
	}
	_ = proc.writeLine(rpcResponseOut{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}
