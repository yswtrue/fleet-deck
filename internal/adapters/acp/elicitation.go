package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// ---- form elicitation ------------------------------------------------------
//
// ACP `elicitation/create` (form mode) asks the user for structured input with
// a flat JSON Schema. The hub's pending-input channel is a string, so the
// request mirrors the permission convention:
//
//	<message>
//
//	[elicitation_request] schema={"type":"object","properties":{…},"required":[…]}
//
// The web renders a form from the schema and replies through the normal
// SendMessage path:
//
//	[elicitation_response] action="accept" content={"name":"octocat"}
//	[elicitation_response] action="decline"
//
// Only form mode is advertised. URL mode and unknown modes are rejected with
// invalid-params before the session pauses, so an agent can fall back instead
// of waiting on a request this client cannot render.

type elicitationParams struct {
	SessionID       string          `json:"sessionId"`
	Mode            string          `json:"mode"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	ToolCallID      string          `json:"toolCallId,omitempty"`
	RequestID       json.RawMessage `json:"requestId,omitempty"`
	URL             string          `json:"url,omitempty"`
	ElicitationID   string          `json:"elicitationId,omitempty"`
}

// elicitationReplyRe unwraps the user's reply. The content group is greedy
// because JSON itself may contain spaces; anything that is not valid JSON is
// rejected during validation and answered with `cancel`.
var elicitationReplyRe = regexp.MustCompile(`(?s)\[elicitation_response\]\s*action="([^"]*)"(?:\s*content=(\{.*\}))?`)

func (a *Adapter) handleElicitationRequest(ctx context.Context, sessionID string, sink adapters.EventSink, msg inbound) {
	var p elicitationParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		a.respondElicitationError(msg.ID, "invalid elicitation params")
		return
	}
	if p.Mode != "form" {
		a.respondElicitationError(msg.ID, "elicitation mode not supported: "+p.Mode)
		return
	}
	if len(p.RequestedSchema) == 0 || len(p.RequestedSchema) > maxElicitationSchemaBytes {
		a.respondElicitationError(msg.ID, "elicitation schema missing or too large")
		return
	}
	if _, err := parseElicitationSchema(p.RequestedSchema); err != nil {
		a.respondElicitationError(msg.ID, err.Error())
		return
	}

	question := strings.TrimSpace(p.Message)
	if question == "" {
		question = "Input requested"
	}
	prompt := question + "\n\n[elicitation_request] schema=" + string(p.RequestedSchema)

	replyCh := make(chan string, 1)
	a.mu.Lock()
	a.awaitingElicit = replyCh
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
		// The agent exited while the form was open: unblock the read loop so
		// the exit watcher can report the terminal state.
		cancelled = true
		exited = true
	case <-ctx.Done():
		cancelled = true
	}

	a.mu.Lock()
	a.awaitingElicit = nil
	if !exited {
		a.status = protocol.SessionRunning
	}
	a.mu.Unlock()

	if exited {
		// The exit watcher owns the terminal event; writing a status or a
		// JSON-RPC response here would race it.
		return
	}
	if cancelled {
		a.respond(msg.ID, elicitationOutcome("cancel", nil), nil)
		return
	}

	action, content := parseElicitationReply(reply)
	switch action {
	case "accept":
		if err := validateElicitationContent(p.RequestedSchema, content); err != nil {
			// Never forward malformed or incomplete content under an accept:
			// the agent would treat it as user consent.
			a.log.Warn("acp: invalid elicitation response; cancelling", "err", err)
			a.respond(msg.ID, elicitationOutcome("cancel", nil), nil)
		} else {
			a.respond(msg.ID, elicitationOutcome("accept", content), nil)
		}
	case "decline":
		a.respond(msg.ID, elicitationOutcome("decline", nil), nil)
	default:
		a.respond(msg.ID, elicitationOutcome("cancel", nil), nil)
	}

	if sink != nil {
		sink.StateChange(ctx, sessionID, protocol.SessionRunning)
	}
}

func (a *Adapter) respondElicitationError(id json.RawMessage, message string) {
	a.respond(id, nil, &rpcError{Code: -32602, Message: message})
}

func elicitationOutcome(action string, content json.RawMessage) map[string]any {
	out := map[string]any{"action": action}
	if action == "accept" && len(content) > 0 {
		out["content"] = content
	}
	return out
}

// parseElicitationReply returns the action and optional content object. An
// unparseable reply yields an empty action, which the caller answers with
// `cancel`.
func parseElicitationReply(reply string) (string, json.RawMessage) {
	m := elicitationReplyRe.FindStringSubmatch(reply)
	if len(m) != 3 {
		return "", nil
	}
	action := strings.ToLower(strings.TrimSpace(m[1]))
	content := strings.TrimSpace(m[2])
	if content == "" {
		return action, nil
	}
	return action, json.RawMessage(content)
}

// ---- schema validation -----------------------------------------------------
//
// The accepted subset is the ACP/MCP form schema: a flat object whose
// properties are string (optionally enum/oneOf), number, integer, boolean, or
// an array of enum strings. Nested objects and arrays of objects are rejected
// before the session pauses; rendering them as a known control would be wrong.

type elicitationSchemaSpec struct {
	Type       string                             `json:"type"`
	Properties map[string]elicitationPropertySpec `json:"properties"`
	Required   []string                           `json:"required"`
}

type elicitationPropertySpec struct {
	Type        string                   `json:"type"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Format      string                   `json:"format"`
	Default     json.RawMessage          `json:"default"`
	Enum        []string                 `json:"enum"`
	OneOf       []elicitationEnumEntry   `json:"oneOf"`
	AnyOf       []elicitationEnumEntry   `json:"anyOf"`
	Items       *elicitationPropertySpec `json:"items"`
	MinLength   *int                     `json:"minLength"`
	MaxLength   *int                     `json:"maxLength"`
	Minimum     *float64                 `json:"minimum"`
	Maximum     *float64                 `json:"maximum"`
	MinItems    *int                     `json:"minItems"`
	MaxItems    *int                     `json:"maxItems"`
}

type elicitationEnumEntry struct {
	Const json.RawMessage `json:"const"`
	Title string          `json:"title"`
}

func parseElicitationSchema(raw json.RawMessage) (elicitationSchemaSpec, error) {
	var spec elicitationSchemaSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("elicitation schema is not a supported object: %w", err)
	}
	if spec.Type != "" && spec.Type != "object" {
		return spec, fmt.Errorf("elicitation schema type %q is not object", spec.Type)
	}
	for name, prop := range spec.Properties {
		if err := validatePropertySchema(prop); err != nil {
			return spec, fmt.Errorf("elicitation property %q: %w", name, err)
		}
		if len(prop.Default) > 0 && string(prop.Default) != "null" {
			if err := validatePropertyValue(prop, prop.Default); err != nil {
				return spec, fmt.Errorf("elicitation property %q default: %w", name, err)
			}
		}
	}
	for _, name := range spec.Required {
		if _, ok := spec.Properties[name]; !ok {
			return spec, fmt.Errorf("elicitation schema requires unknown property %q", name)
		}
	}
	return spec, nil
}

func validatePropertySchema(prop elicitationPropertySpec) error {
	switch prop.Type {
	case "string":
		if _, err := enumValues(prop); err != nil {
			return err
		}
		if prop.MinLength != nil && *prop.MinLength < 0 {
			return fmt.Errorf("minLength is negative")
		}
		if prop.MaxLength != nil && prop.MinLength != nil && *prop.MaxLength < *prop.MinLength {
			return fmt.Errorf("maxLength is smaller than minLength")
		}
	case "number", "integer":
		if prop.Minimum != nil && prop.Maximum != nil && *prop.Maximum < *prop.Minimum {
			return fmt.Errorf("maximum is smaller than minimum")
		}
	case "boolean":
	case "array":
		if prop.Items == nil {
			return fmt.Errorf("array property has no items schema")
		}
		if prop.Items.Type != "" && prop.Items.Type != "string" {
			return fmt.Errorf("only arrays of enum strings are supported")
		}
		options, err := enumValues(*prop.Items)
		if err != nil {
			return err
		}
		if len(options) == 0 {
			return fmt.Errorf("arrays must carry an enum or anyOf/oneOf")
		}
		if prop.MinItems != nil && *prop.MinItems < 0 {
			return fmt.Errorf("minItems is negative")
		}
		if prop.MaxItems != nil && prop.MinItems != nil && *prop.MaxItems < *prop.MinItems {
			return fmt.Errorf("maxItems is smaller than minItems")
		}
	default:
		return fmt.Errorf("unsupported property type %q", prop.Type)
	}
	return nil
}

// enumValues returns the offered labels for a string/multi-select property.
// An empty schema with no enum/oneOf is a free-form string, not an error.
func enumValues(prop elicitationPropertySpec) ([]string, error) {
	sources := 0
	if len(prop.Enum) > 0 {
		sources++
	}
	if len(prop.OneOf) > 0 {
		sources++
	}
	if len(prop.AnyOf) > 0 {
		sources++
	}
	if sources > 1 {
		return nil, fmt.Errorf("enum, oneOf, and anyOf are mutually exclusive")
	}
	if len(prop.Enum) > 0 {
		return prop.Enum, nil
	}
	entries := prop.OneOf
	if len(entries) == 0 {
		entries = prop.AnyOf
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		var value string
		if err := json.Unmarshal(entry.Const, &value); err != nil {
			return nil, fmt.Errorf("enum option const is not a string")
		}
		out = append(out, value)
	}
	return out, nil
}

// validateElicitationContent checks an accept payload against the schema.
// Unknown keys, missing required keys, wrong primitive types, and enum
// violations all fail; the caller answers `cancel` rather than forwarding
// content the agent did not ask for.
func validateElicitationContent(schemaRaw, content json.RawMessage) error {
	spec, err := parseElicitationSchema(schemaRaw)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		content = json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return fmt.Errorf("content is not a JSON object: %w", err)
	}
	for key := range object {
		if _, ok := spec.Properties[key]; !ok {
			return fmt.Errorf("content has unknown property %q", key)
		}
	}
	for _, name := range spec.Required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("content is missing required property %q", name)
		}
	}
	for name, raw := range object {
		if err := validatePropertyValue(spec.Properties[name], raw); err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}
	}
	return nil
}

func validatePropertyValue(prop elicitationPropertySpec, raw json.RawMessage) error {
	switch prop.Type {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("not a string")
		}
		if options, err := enumValues(prop); err != nil {
			return err
		} else if len(options) > 0 && !containsString(options, value) {
			return fmt.Errorf("value %q is not an offered option", value)
		}
		length := utf8.RuneCountInString(value)
		if prop.MinLength != nil && length < *prop.MinLength {
			return fmt.Errorf("shorter than minLength")
		}
		if prop.MaxLength != nil && length > *prop.MaxLength {
			return fmt.Errorf("longer than maxLength")
		}
	case "number", "integer":
		var value json.Number
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("not a number")
		}
		number, err := value.Float64()
		if err != nil {
			return fmt.Errorf("not a number")
		}
		if prop.Type == "integer" && math.Trunc(number) != number {
			return fmt.Errorf("not an integer")
		}
		if prop.Minimum != nil && number < *prop.Minimum {
			return fmt.Errorf("below minimum")
		}
		if prop.Maximum != nil && number > *prop.Maximum {
			return fmt.Errorf("above maximum")
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("not a boolean")
		}
	case "array":
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("not an array")
		}
		if prop.MinItems != nil && len(values) < *prop.MinItems {
			return fmt.Errorf("fewer than minItems")
		}
		if prop.MaxItems != nil && len(values) > *prop.MaxItems {
			return fmt.Errorf("more than maxItems")
		}
		var options []string
		if prop.Items != nil {
			var err error
			options, err = enumValues(*prop.Items)
			if err != nil {
				return err
			}
		}
		for _, item := range values {
			var value string
			if err := json.Unmarshal(item, &value); err != nil {
				return fmt.Errorf("array item is not a string")
			}
			if len(options) > 0 && !containsString(options, value) {
				return fmt.Errorf("array item %q is not an offered option", value)
			}
		}
	default:
		return fmt.Errorf("unsupported property type %q", prop.Type)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
