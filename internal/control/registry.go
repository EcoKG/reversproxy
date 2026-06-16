package control

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// Client represents a connected control-plane client.
type Client struct {
	ID            string
	Name          string
	Addr          string
	RegisteredAt  time.Time
	lastHeartbeat atomic.Value // stores time.Time
	Conn          net.Conn
	// Writer serialises all protocol writes to Conn. Every writer (heartbeat,
	// proxy OpenConnection sends, control responses, disconnect) must use it so
	// frames cannot interleave on the wire.
	Writer   *tunnel.CtrlConnWriter
	cancelFn context.CancelFunc
}

// SetLastHeartbeat atomically updates the last heartbeat timestamp.
func (c *Client) SetLastHeartbeat(t time.Time) { c.lastHeartbeat.Store(t) }

// LastHeartbeat atomically reads the last heartbeat timestamp.
func (c *Client) LastHeartbeat() time.Time {
	v := c.lastHeartbeat.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

// ClientRegistry is a thread-safe registry of connected clients.
// All read operations are protected by an RLock; writes use a full Lock.
type ClientRegistry struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

// NewClientRegistry returns an initialised, empty ClientRegistry.
func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{
		clients: make(map[string]*Client),
	}
}

// Register creates a new Client with a freshly-generated UUID, adds it to the
// registry, and returns a pointer to it. cancelFn is stored on the Client so
// that heartbeat or handler goroutines can cancel the client's context.
func (r *ClientRegistry) Register(name, addr string, conn net.Conn, cancelFn context.CancelFunc) *Client {
	client := &Client{
		ID:           uuid.New().String(),
		Name:         name,
		Addr:         addr,
		RegisteredAt: time.Now(),
		Conn:         conn,
		Writer:       tunnel.NewCtrlConnWriter(conn),
		cancelFn:     cancelFn,
	}
	client.SetLastHeartbeat(time.Now())

	r.mu.Lock()
	r.clients[client.ID] = client
	r.mu.Unlock()

	return client
}

// Disconnect forcibly tears down the client identified by id: it cancels the
// client's context and closes its control connection, which triggers the
// deferred cleanup in HandleControlConn (tunnel removal, deregistration).
// Returns true if a client with that id was found. Safe on an already-closed
// connection. Intended for admin/UI-initiated disconnects.
func (r *ClientRegistry) Disconnect(id string) bool {
	r.mu.RLock()
	c, ok := r.clients[id]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if c.cancelFn != nil {
		c.cancelFn()
	}
	if c.Conn != nil {
		_ = c.Conn.Close()
	}
	return true
}

// Deregister removes the client identified by id from the registry.
// It is a no-op if the id is not present.
func (r *ClientRegistry) Deregister(id string) {
	r.mu.Lock()
	delete(r.clients, id)
	r.mu.Unlock()
}

// Get returns the client for the given id. The second return value is false
// when no client with that id is registered.
func (r *ClientRegistry) Get(id string) (*Client, bool) {
	r.mu.RLock()
	client, ok := r.clients[id]
	r.mu.RUnlock()
	return client, ok
}

// List returns a snapshot of all currently-registered clients. The slice may
// be empty but is never nil.
func (r *ClientRegistry) List() []*Client {
	r.mu.RLock()
	out := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, c)
	}
	r.mu.RUnlock()
	return out
}
