// Package stats provides thread-safe traffic counters for the reverse proxy.
// All global counters use sync/atomic for lock-free reads and writes.
// Per-tunnel counters are tracked in TunnelStats, which is also atomic.
package stats

import (
	"io"
	"sync"
	"sync/atomic"
)

// Global holds the server-wide traffic statistics.
var Global = &ServerStats{}

// ServerStats tracks aggregate traffic counters across all tunnels.
type ServerStats struct {
	TotalConnections  atomic.Int64
	ActiveConnections atomic.Int64
	BytesIn           atomic.Int64
	BytesOut          atomic.Int64
}

// TunnelStats tracks per-tunnel traffic counters.
type TunnelStats struct {
	ConnectionCount atomic.Int64
	BytesIn         atomic.Int64
	BytesOut        atomic.Int64
}

// Registry is a thread-safe map of tunnelID → *TunnelStats.
type Registry struct {
	mu    sync.RWMutex
	stats map[string]*TunnelStats
}

// NewRegistry returns an initialised, empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		stats: make(map[string]*TunnelStats),
	}
}

// GetOrCreate returns an existing TunnelStats for tunnelID, or creates a new
// one and inserts it into the registry.
func (r *Registry) GetOrCreate(tunnelID string) *TunnelStats {
	r.mu.RLock()
	ts, ok := r.stats[tunnelID]
	r.mu.RUnlock()
	if ok {
		return ts
	}

	r.mu.Lock()
	// Double-check after acquiring write lock.
	if ts, ok = r.stats[tunnelID]; ok {
		r.mu.Unlock()
		return ts
	}
	ts = &TunnelStats{}
	r.stats[tunnelID] = ts
	r.mu.Unlock()
	return ts
}

// Get returns the TunnelStats for tunnelID, or nil if not registered.
func (r *Registry) Get(tunnelID string) (*TunnelStats, bool) {
	r.mu.RLock()
	ts, ok := r.stats[tunnelID]
	r.mu.RUnlock()
	return ts, ok
}

// Delete removes the TunnelStats for tunnelID.
func (r *Registry) Delete(tunnelID string) {
	r.mu.Lock()
	delete(r.stats, tunnelID)
	r.mu.Unlock()
}

// Snapshot returns a point-in-time copy of all per-tunnel stats.
func (r *Registry) Snapshot() map[string]TunnelSnapshot {
	r.mu.RLock()
	out := make(map[string]TunnelSnapshot, len(r.stats))
	for id, ts := range r.stats {
		out[id] = TunnelSnapshot{
			ConnectionCount: ts.ConnectionCount.Load(),
			BytesIn:         ts.BytesIn.Load(),
			BytesOut:        ts.BytesOut.Load(),
		}
	}
	r.mu.RUnlock()
	return out
}

// TunnelSnapshot is a point-in-time read of a TunnelStats.
type TunnelSnapshot struct {
	ConnectionCount int64 `json:"connection_count"`
	BytesIn         int64 `json:"bytes_in"`
	BytesOut        int64 `json:"bytes_out"`
}

// -----------------------------------------------------------------------
// Counted I/O wrappers
// -----------------------------------------------------------------------

// CountedReader wraps an io.Reader and counts bytes read into BytesIn on the
// provided *TunnelStats and the global ServerStats.
type CountedReader struct {
	r       io.Reader
	tunnel  *TunnelStats
	global  *ServerStats
}

// NewCountedReader wraps r so all reads are accounted in ts and in the global
// ServerStats. Pass nil for ts to only update global counters.
func NewCountedReader(r io.Reader, ts *TunnelStats, gs *ServerStats) *CountedReader {
	if gs == nil {
		gs = Global
	}
	return &CountedReader{r: r, tunnel: ts, global: gs}
}

func (c *CountedReader) Read(p []byte) (n int, err error) {
	n, err = c.r.Read(p)
	if n > 0 {
		c.global.BytesIn.Add(int64(n))
		if c.tunnel != nil {
			c.tunnel.BytesIn.Add(int64(n))
		}
	}
	return
}

// CountedWriter wraps an io.Writer and counts bytes written into BytesOut on
// the provided *TunnelStats and the global ServerStats.
type CountedWriter struct {
	w      io.Writer
	tunnel *TunnelStats
	global *ServerStats
}

// NewCountedWriter wraps w so all writes are accounted in ts and in the global
// ServerStats. Pass nil for ts to only update global counters.
func NewCountedWriter(w io.Writer, ts *TunnelStats, gs *ServerStats) *CountedWriter {
	if gs == nil {
		gs = Global
	}
	return &CountedWriter{w: w, tunnel: ts, global: gs}
}

func (c *CountedWriter) Write(p []byte) (n int, err error) {
	n, err = c.w.Write(p)
	if n > 0 {
		c.global.BytesOut.Add(int64(n))
		if c.tunnel != nil {
			c.tunnel.BytesOut.Add(int64(n))
		}
	}
	return
}
