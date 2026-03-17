// Package reconnect provides exponential backoff and tunnel state preservation
// for client auto-reconnection.
package reconnect

import (
	"math/rand"
	"sync"
	"time"
)

// Backoff implements an exponential backoff strategy with optional jitter.
// It is safe for concurrent use.
//
//   - Initial: delay returned on the first call to Next after a Reset.
//   - Max: upper bound on the delay (before jitter).
//   - Multiplier: factor by which the delay grows each call.
//   - JitterFraction: fraction of the delay added as random noise (e.g. 0.2 = ±20%).
type Backoff struct {
	Initial         time.Duration
	Max             time.Duration
	Multiplier      float64
	JitterFraction  float64

	mu      sync.Mutex
	current time.Duration
}

// NewBackoff returns a Backoff with sensible defaults for reconnection:
// start at 1 s, cap at 60 s, multiply by 2, ±20% jitter.
func NewBackoff() *Backoff {
	return &Backoff{
		Initial:        1 * time.Second,
		Max:            60 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0.2,
	}
}

// Next returns the next delay to wait before retrying, then advances the
// internal state. Jitter (±JitterFraction * delay) is applied so that
// multiple clients do not all reconnect simultaneously (thundering herd).
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.current == 0 {
		b.current = b.Initial
	}

	delay := b.current

	// Advance: clamp at Max before jitter so the cap is predictable.
	next := time.Duration(float64(b.current) * b.Multiplier)
	if next > b.Max {
		next = b.Max
	}
	b.current = next

	// Apply ±JitterFraction jitter.
	if b.JitterFraction > 0 {
		jitter := time.Duration(float64(delay) * b.JitterFraction * (rand.Float64()*2 - 1))
		delay += jitter
		if delay < 0 {
			delay = 0
		}
	}

	return delay
}

// Reset restores the delay to its initial value. Call this after a
// successful connection so the next disconnect starts from scratch.
func (b *Backoff) Reset() {
	b.mu.Lock()
	b.current = 0
	b.mu.Unlock()
}
