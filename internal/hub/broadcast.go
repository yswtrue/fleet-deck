package hub

import (
	"sync"
	"sync/atomic"
	"time"

	"fleetdeck/internal/protocol"
)

// subscriberBuffer is the per-subscriber queue depth. A subscriber that falls
// this far behind drops events rather than blocking the hub (Phase 3 clients
// reconnect and gap-fill through /api/sessions/{id}/events).
const subscriberBuffer = 64

// EventScope decides which events one subscriber receives. Web connections
// scope by node visibility (see Hub.webEventScope); internal consumers and
// tests use EventScope{All: true}.
type EventScope struct {
	// All receives every event, including events whose owning node could not
	// be resolved. Admin subscribers use it.
	All bool

	// Visible reports whether the subscriber may observe nodeID's subtree.
	// Consulted only when All is false, and never with an empty node id.
	Visible func(nodeID string) bool
}

// allows reports whether this scope receives an event owned by nodeID. An
// empty nodeID (an event whose owning node could not be resolved) is
// admin-only: non-admin subscribers drop rather than risk leaking another
// user's subtree.
func (s EventScope) allows(nodeID string) bool {
	if s.All {
		return true
	}
	if nodeID == "" {
		return false
	}
	return s.Visible != nil && s.Visible(nodeID)
}

// subscriber is one registered web client connection.
type subscriber struct {
	ch    chan protocol.Event
	scope EventScope
}

// Broadcaster fans hub events out to subscribed web clients, scoped per
// subscriber by node visibility. It also owns the single global sequence
// counter stamped onto every pushed protocol.Event.
//
// Note: Event.Seq here is a hub-wide broadcast ordinal, not the per-session seq
// persisted on the event table (store.Event.Seq, used by the gap-fill API).
// Seq is stamped before scoping, so a filtered subscriber observes gaps in its
// stream and gap-fills through the permission-filtered /api/sessions/{id}/events.
type Broadcaster struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
	seq    atomic.Int64

	// resolve maps an event onto the node id whose subtree owns it ("" when
	// unresolvable). It runs exactly once per published event, before fan-out,
	// so no subscriber triggers its own lookup.
	resolve func(protocol.Event) string
}

// NewBroadcaster returns an empty broadcaster. resolve maps an event to its
// owning node id ("" = unresolvable); nil treats every event as unresolvable,
// delivering only to All-scopes.
func NewBroadcaster(resolve func(protocol.Event) string) *Broadcaster {
	return &Broadcaster{subs: make(map[*subscriber]struct{}), resolve: resolve}
}

// NextSeq returns the next global broadcast sequence number.
func (b *Broadcaster) NextSeq() int64 { return b.seq.Add(1) }

// Subscribe registers a new subscriber under the given scope. The returned
// unsubscribe function is idempotent and closes the channel.
func (b *Broadcaster) Subscribe(scope EventScope) (<-chan protocol.Event, func()) {
	ch := make(chan protocol.Event, subscriberBuffer)
	sub := &subscriber{ch: ch, scope: scope}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[sub]; ok {
				delete(b.subs, sub)
				close(ch)
			}
			b.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// Publish stamps evt with the next global Seq (and Ts if unset), resolves the
// owning node once, and fans it out to the subscribers whose scope covers that
// node. It never blocks: a full subscriber queue causes that event to be
// dropped for that subscriber only.
func (b *Broadcaster) Publish(evt protocol.Event) {
	if evt.Ts.IsZero() {
		evt.Ts = time.Now().UTC()
	}
	evt.Seq = b.NextSeq()

	nodeID := ""
	if b.resolve != nil {
		nodeID = b.resolve(evt)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for sub := range b.subs {
		if !sub.scope.allows(nodeID) {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
		}
	}
}

// Close disconnects all subscribers. It is safe to call more than once.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		delete(b.subs, sub)
		close(sub.ch)
	}
}
