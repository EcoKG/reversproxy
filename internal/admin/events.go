package admin

import (
	"sync"
	"time"
)

// Event is a structured server-side event broadcast over the SSE channel.
// Type is one of: client.connected, client.disconnected, client.pending,
// client.approved, client.rejected, client.fingerprint_mismatch.
type Event struct {
	Type   string    `json:"type"`
	Name   string    `json:"name,omitempty"`
	Addr   string    `json:"addr,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Time   time.Time `json:"time"`
}

// EventBus is a non-blocking fan-out broadcaster: subscribers each have a
// bounded buffered channel, slow consumers drop events instead of stalling
// publishers.
type EventBus struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	bufSize     int
}

// NewEventBus returns a bus that gives each subscriber a channel of the given
// buffer size.
func NewEventBus(bufSize int) *EventBus {
	if bufSize <= 0 {
		bufSize = 16
	}
	return &EventBus{
		subscribers: make(map[chan Event]struct{}),
		bufSize:     bufSize,
	}
}

// Publish delivers ev to every subscriber on a best-effort basis. If a
// subscriber's buffer is full the event is dropped for that subscriber.
func (b *EventBus) Publish(ev Event) {
	if b == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b.mu.Lock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
	b.mu.Unlock()
}

// Subscribe returns a new channel that receives published events plus a
// cleanup func the caller must invoke when done.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	if b == nil {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan Event, b.bufSize)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
		close(ch)
	}
}
