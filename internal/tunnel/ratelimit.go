package tunnel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ipLimiter is a simple per-IP token bucket rate limiter.
type ipLimiter struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
	mu       sync.Mutex
	rate     float64
	burst    int
}

func (l *ipLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.lastSeen = now
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
	ttl       time.Duration
	active    atomic.Int64
	ips       sync.Map // map[string]*ipLimiter
}

// NewLimiter creates a new Limiter. maxRate is the per-IP rate in
// connections/sec. burst is the max burst. maxGlobal is the max concurrent
// connections (0 = unlimited). ttl is the idle TTL for per-IP entries
// (entries not seen within ttl are removed by the cleanup goroutine;
// 0 uses the default of 5 minutes). If maxRate <= 0, per-IP rate limiting
// is disabled.
func NewLimiter(maxRate float64, burst int, maxGlobal int64, ttl time.Duration) *Limiter {
	if burst <= 0 {
		burst = 10
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Limiter{
		maxRate:   maxRate,
		burst:     burst,
		maxGlobal: maxGlobal,
		ttl:       ttl,
	}
}

// StartCleanup launches a background goroutine that removes per-IP entries
// whose lastSeen timestamp is older than l.ttl. The goroutine runs once per
// minute and stops when ctx is cancelled.
func (l *Limiter) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cutoff := now.Add(-l.ttl)
				l.ips.Range(func(key, value any) bool {
					lim := value.(*ipLimiter)
					lim.mu.Lock()
					expired := lim.lastSeen.Before(cutoff)
					lim.mu.Unlock()
					if expired {
						l.ips.Delete(key)
					}
					return true
				})
			}
		}
	}()
}

// Allow checks whether a connection from ip should be allowed based on
// per-IP rate limiting.
func (l *Limiter) Allow(ip string) bool {
	if l.maxRate <= 0 {
		return true
	}

	now := time.Now()
	val, loaded := l.ips.LoadOrStore(ip, &ipLimiter{
		tokens:   float64(l.burst),
		last:     now,
		lastSeen: now,
		rate:     l.maxRate,
		burst:    l.burst,
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
