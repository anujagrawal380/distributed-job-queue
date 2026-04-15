package queue

import (
	"sync"
)

// EventKind is the type of queue event emitted to subscribers.
type EventKind string

const (
	EventSubmitted EventKind = "submitted"
	EventLeased    EventKind = "leased"
	EventAcked     EventKind = "acked"
	EventRetry     EventKind = "retry"
	EventDead      EventKind = "dead"
)

// Event is a lightweight notification of a state change, suitable for SSE.
type Event struct {
	Kind  EventKind `json:"kind"`
	JobID string    `json:"job_id"`
	State JobState  `json:"state"`
}

// eventBus is an internal fan-out bus for queue events. Subscribers that can't
// keep up have events dropped rather than blocking the producer.
type eventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of events and an unsubscribe func. The channel
// is buffered; if a subscriber is slow, events are dropped.
func (b *eventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 128)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	unsub := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, unsub
}

func (b *eventBus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			// subscriber is slow; drop this event rather than block the producer
		}
	}
}
