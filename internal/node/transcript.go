package node

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/protocol"
)

// liveTranscriptTTL bounds how long a session's transcript stays cached on the
// node. The cache is in-memory only (session content never touches the hub's
// database); a finished session's durable record is still the agent's own
// session store, which the hub replays on demand. Keeping the cache around for
// a day lets the browser read the exact live ordering (including tool calls)
// and covers a hub restart without silently truncating the transcript.
const liveTranscriptTTL = 24 * time.Hour

// transcriptSweepInterval throttles expired-entry purging: appending streamed
// deltas is hot, so the cache must not walk every session per chunk.
const transcriptSweepInterval = time.Minute

// liveTranscript is one session's ordered live transcript. messages is the
// authoritative order the browser renders: user prompts, complete assistant
// messages, and tool calls interleaved as they happened. partial is the text
// of the in-progress turn (text deltas are not messages until the adapter
// completes the turn); pending is the unanswered user-input ask.
type liveTranscript struct {
	messages []protocol.Message
	// index maps a message id to its slice position. Tool updates and repeated
	// adapter message ids replace the row in place instead of appending.
	index map[string]int
	// partial accumulates agent_message chunks for the running turn.
	partial strings.Builder
	// thought is the in-progress turn's reasoning, bounded to the same window
	// the web renders. It is memory-only: a page reload reads it back through
	// the snapshot instead of losing everything the agent already thought.
	thought string
	// pending is the prompt of the outstanding user-input ask.
	pending string
	// run distinguishes adapter generations after a resume: adapter-side
	// counters restart, so ids are scoped to this run.
	run uint64
	// nextID is the per-run message counter.
	nextID uint64
	// updated is the last write; entries idle past liveTranscriptTTL are
	// dropped.
	updated time.Time
}

// transcriptStore is the node's per-session live transcript cache. Every
// adapter event is appended here before it is enqueued for the hub, so the
// node always holds the complete, ordered conversation regardless of hub
// restarts, dropped WebSocket frames, or a browser opening the session late.
type transcriptStore struct {
	mu        sync.Mutex
	sessions  map[string]*liveTranscript
	lastSweep time.Time
}

func newTranscriptStore() *transcriptStore {
	return &transcriptStore{sessions: make(map[string]*liveTranscript)}
}

// beginRun starts a new adapter generation for a session (initial task start
// or a resume) and resets the per-turn state. Previous messages stay in the
// transcript: a resumed session continues the same conversation.
func (s *transcriptStore) beginRun(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	t.run++
	t.nextID = 0
	t.partial.Reset()
	t.thought = ""
	t.pending = ""
	t.updated = time.Now()
}

// addUser appends a user prompt/reply. The hub supplies an id and timestamp so
// its live broadcast and the node transcript agree; a missing id is minted
// here for older hubs. It returns the stored row so a caller that must echo it
// to the hub uses the same id the transcript holds.
func (s *transcriptStore) addUser(sessionID string, msg protocol.Message) protocol.Message {
	if s == nil {
		return msg
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	if msg.Content == "" {
		return msg
	}
	msg.SessionID = sessionID
	msg.Role = protocol.RoleUser
	if msg.ID == "" {
		msg.ID = t.nextMessageID(sessionID, "msg")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	s.appendLocked(t, msg)
	return msg
}

// addMessage appends a complete assistant message and clears the streaming
// partial it supersedes. The adapter's id is replaced with a node-minted one:
// adapter counters restart with every resume, so keeping them would collide
// with an earlier run's rows.
func (s *transcriptStore) addMessage(sessionID string, msg protocol.Message) protocol.Message {
	if s == nil {
		return msg
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	msg.SessionID = sessionID
	msg.ID = t.nextMessageID(sessionID, "msg")
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	t.partial.Reset()
	t.thought = ""
	s.appendLocked(t, msg)
	return msg
}

// upsertTool records or updates one tool call. ACP sends tool_call first and
// tool_call_update frames afterwards; the update carries only the fields that
// changed, so empty values leave the stored value alone. The row is created at
// the point the tool first appears, which preserves the text/tool interleaving.
func (s *transcriptStore) upsertTool(sessionID string, call adapters.ToolCall) (protocol.Message, bool) {
	if s == nil || call.ID == "" {
		return protocol.Message{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	id := fmt.Sprintf("tool-%s-%d-%s", sessionID, t.run, call.ID)
	if idx, ok := t.index[id]; ok {
		m := &t.messages[idx]
		// Replace the pointer instead of mutating it: an earlier envelope for
		// this row may still be queued for the hub writer, and mutating the
		// shared ToolCall would race with its JSON encoding.
		next := protocol.ToolCall{ID: call.ID}
		if m.Tool != nil {
			next = *m.Tool
		}
		if call.Title != "" {
			next.Title = call.Title
			m.Content = call.Title
		}
		if call.Kind != "" {
			next.Kind = call.Kind
		}
		if call.Status != "" {
			next.Status = call.Status
		}
		m.Tool = &next
		t.updated = time.Now()
		return cloneMessage(*m), true
	}
	tool := &protocol.ToolCall{ID: call.ID, Title: call.Title, Kind: call.Kind, Status: call.Status}
	msg := protocol.Message{
		ID:        id,
		SessionID: sessionID,
		Role:      protocol.RoleTool,
		Content:   call.Title,
		Tool:      tool,
		CreatedAt: time.Now().UTC(),
	}
	s.appendLocked(t, msg)
	return cloneMessage(msg), true
}

// appendDelta accumulates streamed assistant text for the in-progress turn.
// It is not an ordered message: the adapter collapses a turn's chunks into one
// complete Message, and the browser renders this text after the transcript
// until that message arrives.
func (s *transcriptStore) appendDelta(sessionID, text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	t.partial.WriteString(text)
	t.updated = time.Now()
}

// maxLiveThought bounds the buffered reasoning window, matching what the web
// renders. Older chunks fall off so a long chain of thought cannot grow the
// node's memory without bound.
const maxLiveThought = 4000

// appendThought accumulates the in-progress turn's reasoning. It is not an
// ordered message: the browser shows it in the live trace until the turn's
// assistant message (or the agent replay) supersedes it.
func (s *transcriptStore) appendThought(sessionID, text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	t.thought += text
	if len(t.thought) > maxLiveThought {
		cut := len(t.thought) - maxLiveThought
		for cut < len(t.thought) && !utf8.RuneStart(t.thought[cut]) {
			cut++
		}
		t.thought = t.thought[cut:]
	}
	t.updated = time.Now()
}

// clearPartial drops the in-progress turn's text (the complete message or a
// terminal state superseded it).
func (s *transcriptStore) clearPartial(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.sessions[sessionID]; ok {
		t.partial.Reset()
		t.thought = ""
		t.updated = time.Now()
	}
}

// setPending records (or, with an empty prompt, clears) the unanswered ask.
func (s *transcriptStore) setPending(sessionID, prompt string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getLocked(sessionID, time.Now())
	t.pending = prompt
	t.updated = time.Now()
}

// finishTurn records a terminal session state: the stream is over and any ask
// was resolved. The transcript itself stays readable.
func (s *transcriptStore) finishSession(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.sessions[sessionID]; ok {
		t.partial.Reset()
		t.thought = ""
		t.pending = ""
		t.updated = time.Now()
	}
}

// snapshot returns a copy of the session's transcript state. A missing entry
// is an empty transcript, not an error: a live session may not have produced
// anything yet.
func (s *transcriptStore) snapshot(sessionID string) (msgs []protocol.Message, partial, thought, pending string) {
	msgs = []protocol.Message{}
	if s == nil {
		return msgs, "", "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	t, ok := s.sessions[sessionID]
	if !ok {
		return msgs, "", "", ""
	}
	msgs = make([]protocol.Message, len(t.messages))
	for i, m := range t.messages {
		msgs[i] = cloneMessage(m)
	}
	return msgs, t.partial.String(), t.thought, t.pending
}

func (s *transcriptStore) getLocked(sessionID string, now time.Time) *liveTranscript {
	if s.sessions == nil {
		s.sessions = make(map[string]*liveTranscript)
	}
	if t, ok := s.sessions[sessionID]; ok {
		s.sweepLocked(now)
		return t
	}
	t := &liveTranscript{index: make(map[string]int), updated: now}
	s.sessions[sessionID] = t
	s.sweepLocked(now)
	return t
}

func (s *transcriptStore) appendLocked(t *liveTranscript, msg protocol.Message) {
	if t.index == nil {
		t.index = make(map[string]int)
	}
	if idx, ok := t.index[msg.ID]; ok {
		// A repeated id is an update, never a second row.
		t.messages[idx] = msg
	} else {
		t.index[msg.ID] = len(t.messages)
		t.messages = append(t.messages, msg)
	}
	t.updated = time.Now()
}

// sweepLocked drops idle sessions. It runs at most once per interval, so hot
// delta streams do not pay an O(sessions) walk per chunk.
func (s *transcriptStore) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < transcriptSweepInterval {
		return
	}
	s.lastSweep = now
	for id, t := range s.sessions {
		if now.Sub(t.updated) > liveTranscriptTTL {
			delete(s.sessions, id)
		}
	}
}

func (t *liveTranscript) nextMessageID(sessionID, prefix string) string {
	t.nextID++
	return fmt.Sprintf("%s-%s-%d-%d", prefix, sessionID, t.run, t.nextID)
}

func cloneMessage(m protocol.Message) protocol.Message {
	if m.Tool != nil {
		tool := *m.Tool
		m.Tool = &tool
	}
	return m
}
