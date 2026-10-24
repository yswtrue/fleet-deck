package hub

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/protocol"
)

// sessionTextTTL bounds how long a session's buffered text state survives
// without a fresh write or read. The buffer is the only server-side trace of
// session content and exists purely so live sessions survive page reloads and
// WS gaps: the durable record of a conversation is the agent's own session
// store on the node, replayed on demand (see history.go).
//
// The buffer is hub-local memory, deliberately: content never touches
// Postgres or Redis, and its loss (hub restart) costs a live transcript, not
// correctness — history for finished sessions replays from the node.
const sessionTextTTL = 24 * time.Hour

// sessionText is the buffered text state of one session: the complete
// messages that transited the hub while the session was live, the streaming
// assistant text of the in-progress turn (so a page reload mid-turn does not
// lose it), the in-progress turn's reasoning (bounded, same purpose), plus the
// prompt of the current unanswered user-input ask (empty when none).
type sessionText struct {
	Messages       []protocol.Message `json:"messages"`
	PartialOutput  []byte             `json:"partial_output,omitempty"`
	PartialThought string             `json:"partial_thought,omitempty"`
	PendingInput   string             `json:"pending_input,omitempty"`
}

// textBufferEntry is one TTL'd slot in the hub's text buffer.
type textBufferEntry struct {
	text    sessionText
	expires time.Time
}

// textBuffer returns a session's buffered text state; a miss (or expired
// entry) is the zero value. A hit refreshes the TTL so an actively viewed
// session does not lose its buffer mid-conversation.
func (h *Hub) textBuffer(_ context.Context, sessionID string) sessionText {
	h.textMu.Lock()
	defer h.textMu.Unlock()
	e, ok := h.text[sessionID]
	if !ok {
		return sessionText{}
	}
	if time.Now().After(e.expires) {
		delete(h.text, sessionID)
		return sessionText{}
	}
	e.expires = time.Now().Add(sessionTextTTL)
	h.text[sessionID] = e
	return e.text
}

// updateTextBuffer applies fn to a session's buffered text state and stores
// the result with a fresh TTL. The read-modify-write is serialized on textMu
// so concurrent node events and user replies cannot lose messages. Expired
// entries are purged opportunistically to keep the map bounded.
func (h *Hub) updateTextBuffer(_ context.Context, sessionID string, fn func(*sessionText)) {
	h.textMu.Lock()
	defer h.textMu.Unlock()
	now := time.Now()
	for k, e := range h.text {
		if now.After(e.expires) {
			delete(h.text, k)
		}
	}
	e, ok := h.text[sessionID]
	if ok && now.After(e.expires) {
		delete(h.text, sessionID)
		e, ok = textBufferEntry{}, false
	}
	var st sessionText
	if ok {
		st = e.text
	}
	fn(&st)
	h.text[sessionID] = textBufferEntry{text: st, expires: now.Add(sessionTextTTL)}
}

// appendBufferedMessage records one complete message in the session's buffer
// and clears the streaming partial it supersedes. A message carrying an id
// already in the buffer replaces that row: tool calls are re-sent as their
// status changes, and appending each update would duplicate the tool in the
// fallback transcript.
func (h *Hub) appendBufferedMessage(ctx context.Context, sessionID string, msg protocol.Message) {
	h.updateTextBuffer(ctx, sessionID, func(st *sessionText) {
		for i := range st.Messages {
			if st.Messages[i].ID == msg.ID {
				st.Messages[i] = msg
				st.PartialOutput = nil
				st.PartialThought = ""
				return
			}
		}
		st.Messages = append(st.Messages, msg)
		st.PartialOutput = nil
		st.PartialThought = ""
	})
}

// appendPartialText accumulates one streaming assistant chunk. Delta frames
// are frequent, so this takes the lock directly instead of updateTextBuffer
// (whose opportunistic expiry sweep is O(sessions) and must not run per
// token). The partial is memory-only: it never reaches Postgres.
func (h *Hub) appendPartialText(sessionID, text string) {
	if text == "" {
		return
	}
	h.textMu.Lock()
	defer h.textMu.Unlock()
	now := time.Now()
	e, ok := h.text[sessionID]
	if ok && now.After(e.expires) {
		delete(h.text, sessionID)
		e, ok = textBufferEntry{}, false
	}
	if !ok {
		e = textBufferEntry{text: sessionText{}}
	}
	e.text.PartialOutput = append(e.text.PartialOutput, text...)
	e.expires = now.Add(sessionTextTTL)
	h.text[sessionID] = e
}

// clearPartialText drops the streaming partial and the turn's reasoning (a
// complete assistant message or a terminal turn superseded them).
func (h *Hub) clearPartialText(ctx context.Context, sessionID string) {
	h.updateTextBuffer(ctx, sessionID, func(st *sessionText) {
		st.PartialOutput = nil
		st.PartialThought = ""
	})
}

// maxBufferedThought bounds the buffered reasoning window, matching the web's
// live trace. A long chain of thought must not grow hub memory without bound.
const maxBufferedThought = 4000

// appendThoughtText accumulates one reasoning chunk into the session's buffer.
// Like appendPartialText it takes the lock directly: thought frames can be
// frequent, and the opportunistic expiry sweep is O(sessions).
func (h *Hub) appendThoughtText(sessionID, text string) {
	if text == "" {
		return
	}
	h.textMu.Lock()
	defer h.textMu.Unlock()
	now := time.Now()
	e, ok := h.text[sessionID]
	if ok && now.After(e.expires) {
		delete(h.text, sessionID)
		e, ok = textBufferEntry{}, false
	}
	if !ok {
		e = textBufferEntry{text: sessionText{}}
	}
	e.text.PartialThought += text
	if len(e.text.PartialThought) > maxBufferedThought {
		cut := len(e.text.PartialThought) - maxBufferedThought
		for cut < len(e.text.PartialThought) && !utf8.RuneStart(e.text.PartialThought[cut]) {
			cut++
		}
		e.text.PartialThought = e.text.PartialThought[cut:]
	}
	e.expires = now.Add(sessionTextTTL)
	h.text[sessionID] = e
}

// setPendingInput records (or, with an empty prompt, clears) the session's
// pending user-input ask.
func (h *Hub) setPendingInput(ctx context.Context, sessionID, prompt string) {
	h.updateTextBuffer(ctx, sessionID, func(st *sessionText) {
		st.PendingInput = prompt
	})
}

// recordUserMessage buffers a user reply as a session message (minting its
// hub-side id) and clears any pending ask — the reply answers it. The reply is
// content: it is broadcast live and buffered, never written to Postgres.
func (h *Hub) recordUserMessage(ctx context.Context, sessionID, text string) protocol.Message {
	msg := normalizeMessage(sessionID, protocol.Message{
		SessionID: sessionID,
		Role:      protocol.RoleUser,
		Content:   text,
	}, time.Now().UTC())
	h.updateTextBuffer(ctx, sessionID, func(st *sessionText) {
		st.Messages = append(st.Messages, msg)
		st.PendingInput = ""
	})
	return msg
}

// normalizeMessage fills the hub-owned fields of a message passing through.
// An id minted by the node's transcript is preserved: live broadcasts and the
// transcript read must share it, otherwise the web cannot dedupe an assistant
// or tool row that arrives twice (live echo and refetch). Only an id-less
// message gets a hub-minted one. Timestamps are taken from the producer and
// never fabricated when it reports none.
func normalizeMessage(sessionID string, msg protocol.Message, ts time.Time) protocol.Message {
	msg.SessionID = sessionID
	if msg.ID == "" {
		msg.ID = "msg-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = ts
	}
	return msg
}
