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

// Broadcaster fans hub events out to every subscribed web client. It also owns
// the single global sequence counter stamped onto every pushed protocol.Event.
//
// Note: Event.Seq here is a hub-wide broadcast ordinal, not the per-session seq
// persisted on the event table (store.Event.Seq, used by the gap-fill API).
// Session-scoped ordering/gap-fill is served by /api/sessions/{id}/events.
type Broadcaster struct {
	mu     sync.Mutex
	subs   map[chan protocol.Event]struct{}
	closed bool
	seq    atomic.Int64
}

// NewBroadcaster returns an empty broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[chan protocol.Event]struct{})}
}

// NextSeq returns the next global broadcast sequence number.
func (b *Broadcaster) NextSeq() int64 { return b.seq.Add(1) }

// Subscribe registers a new subscriber. The returned unsubscribe function is
// idempotent and closes the channel.
func (b *Broadcaster) Subscribe() (<-chan protocol.Event, func()) {
	ch := make(chan protocol.Event, subscriberBuffer)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[ch]; ok {
				delete(b.subs, ch)
				close(ch)
			}
			b.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// Publish stamps evt with the next global Seq (and Ts if unset) and fans it out.
// It never blocks: a full subscriber queue causes that event to be dropped for
// that subscriber only.
func (b *Broadcaster) Publish(evt protocol.Event) {
	if evt.Ts.IsZero() {
		evt.Ts = time.Now().UTC()
	}
	evt.Seq = b.NextSeq()

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for ch := range b.subs {
		select {
		case ch <- evt:
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
	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}
}
