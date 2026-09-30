package protocol

import "encoding/json"

// Envelope is the wire framing used by both hub and node over their WS
// connection. It is intentionally distinct from protocol.Event (which is the
// hub→web envelope) so that node-internal command types don't leak into the
// web UI's TypeScript types.
type Envelope struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// NewEnvelope wraps a payload with a message type discriminator.
func NewEnvelope(t string, data any) Envelope {
	return Envelope{Type: t, Data: data}
}

// Decode re-decodes the envelope payload into v. Because Envelope is unmarshaled
// with an interface{} Data field, readers re-marshal before decoding into a
// concrete payload type.
func (e Envelope) Decode(v any) error {
	raw, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// Hub <-> Node message type strings.
const (
	// Node -> Hub.
	MsgRegisterRequest = "register_request"
	MsgTodoUpdate      = "todo_update"
	MsgEvent           = "event"
	MsgSessionEvent    = "session.event"

	// Hub -> Node.
	MsgRegisterResponse = "register_response"
	MsgTaskStart        = "task.start"
	MsgSendMessage      = "session.send_message"
	MsgSessionResume    = "session.resume"
	MsgCancel           = "session.cancel"
	MsgSessionHistory   = "session.history_req"

	// Node -> Hub (responses to hub requests).
	MsgSessionHistoryResp = "session.history_resp"
)
