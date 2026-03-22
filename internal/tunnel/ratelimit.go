package tunnel

import (
	"sync"
	"sync/atomic"
	"time"
)

// ipLimiter is a simple per-IP token bucket rate limiter.
type ipLimiter struct {
	tokens   float64
	last     time.Time
	mu       sync.Mutex
	rate     float64
	burst    int
}

func (l *ipLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens += elapsed * l.rate
	if l.tokens > float64(l.burst) {
		l.tokens = float64(l.burst)
	}

	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

// Limiter provides per-IP rate limiting and global concurrency limiting for
// proxy accept loops.
type Limiter struct {
	maxRate   float64
	burst     int
	maxGlobal int64
	active    atomic.Int64
	ips       sync.Map // map[string]*ipLimiter
}

// NewLimiter creates a new Limiter. maxRate is the per-IP rate in
// connections/sec. burst is the max burst. maxGlobal is the max concurrent
// connections (0 = unlimited). If maxRate <= 0, per-IP rate limiting is
// disabled.
func NewLimiter(maxRate float64, burst int, maxGlobal int64) *Limiter {
	if burst <= 0 {
		burst = 10
	}
	return &Limiter{
		maxRate:   maxRate,
		burst:     burst,
		maxGlobal: maxGlobal,
	}
}

// Allow checks whether a connection from ip should be allowed based on
// per-IP rate limiting.
func (l *Limiter) Allow(ip string) bool {
	if l.maxRate <= 0 {
		return true
	}

	now := time.Now()
	val, loaded := l.ips.LoadOrStore(ip, &ipLimiter{
		tokens: float64(l.burst),
		last:   now,
		rate:   l.maxRate,
		burst:  l.burst,
	})
	lim := val.(*ipLimiter)
	if !loaded {
		// Just created — first request always passes
		lim.mu.Lock()
		lim.tokens--
		lim.mu.Unlock()
		return true
	}
	return lim.allow(now)
}

// Acquire increments the active connection count and returns true if
// the connection is allowed (under the global concurrency limit).
// Returns true if maxGlobal is 0 (unlimited).
func (l *Limiter) Acquire() bool {
	if l.maxGlobal <= 0 {
		l.active.Add(1)
		return true
	}
	for {
		cur := l.active.Load()
		if cur >= l.maxGlobal {
			return false
		}
		if l.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release decrements the active connection count.
func (l *Limiter) Release() {
	l.active.Add(-1)
}
