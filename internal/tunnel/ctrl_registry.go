package tunnel

import (
	"net"
	"sync"
)

// ControlConnRegistry maps clientID → net.Conn (the TLS control connection).
// It is used by the HTTP/HTTPS proxy to send OpenConnection messages to the
// correct client when a routed request arrives.
//
// It is safe for concurrent use.
type ControlConnRegistry struct {
	mu    sync.RWMutex
	conns map[string]net.Conn
}

// NewControlConnRegistry returns an empty, initialised ControlConnRegistry.
func NewControlConnRegistry() *ControlConnRegistry {
	return &ControlConnRegistry{
		conns: make(map[string]net.Conn),
	}
}

// Register associates clientID with conn.
// Any previous registration for clientID is silently overwritten.
func (r *ControlConnRegistry) Register(clientID string, conn net.Conn) {
	r.mu.Lock()
	r.conns[clientID] = conn
	r.mu.Unlock()
}

// Deregister removes the entry for clientID.
func (r *ControlConnRegistry) Deregister(clientID string) {
	r.mu.Lock()
	delete(r.conns, clientID)
	r.mu.Unlock()
}

// Get returns the control connection for clientID, or (nil, false) if not found.
func (r *ControlConnRegistry) Get(clientID string) (net.Conn, bool) {
	r.mu.RLock()
	conn, ok := r.conns[clientID]
	r.mu.RUnlock()
	return conn, ok
}
