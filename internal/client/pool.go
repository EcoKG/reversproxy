package client

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ControlWriter is the minimal write interface for a session's control
// connection. Implemented by *ConnWriter in production and by stubs in tests.
type ControlWriter interface {
	WriteMsg(msgType protocol.MsgType, payload any) error
}

// ConnWriter wraps a net.Conn with a mutex so writes from concurrent
// goroutines (SOCKS relays + control message loop) are serialised.
// One ConnWriter is bound to a single ServerSession's connection for its lifetime.
type ConnWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

// NewConnWriter binds a ConnWriter to c.
func NewConnWriter(c net.Conn) *ConnWriter {
	return &ConnWriter{conn: c}
}

// WriteMsg writes a protocol message to the bound connection.
func (w *ConnWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// ServerSession represents a single connected proxy server. The client may hold
// multiple concurrent sessions when several servers dial in.
type ServerSession struct {
	ID          string
	Conn        net.Conn
	Writer      ControlWriter
	Mux         *tunnel.SOCKSMux
	Addr        string
	ServerName  string
	ConnectedAt time.Time
}

// ServerPool tracks all active server sessions and round-robins over them
// when picking a session for a new client-originated request (SOCKS5,
// HTTP CONNECT, port-forward).
type ServerPool struct {
	mu       sync.RWMutex
	sessions []*ServerSession
	counter  atomic.Uint64
}

func NewServerPool() *ServerPool { return &ServerPool{} }

// Add inserts a session. Safe to call from goroutines.
func (p *ServerPool) Add(s *ServerSession) {
	p.mu.Lock()
	p.sessions = append(p.sessions, s)
	p.mu.Unlock()
}

// Remove drops a session by pointer identity.
func (p *ServerPool) Remove(s *ServerSession) {
	p.mu.Lock()
	out := p.sessions[:0]
	for _, x := range p.sessions {
		if x != s {
			out = append(out, x)
		}
	}
	p.sessions = out
	p.mu.Unlock()
}

// Pick returns the next session in round-robin order, or nil if empty.
func (p *ServerPool) Pick() *ServerSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := uint64(len(p.sessions))
	if n == 0 {
		return nil
	}
	i := p.counter.Add(1) - 1
	return p.sessions[int(i%n)]
}

// List returns a snapshot slice of the current sessions.
func (p *ServerPool) List() []*ServerSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*ServerSession, len(p.sessions))
	copy(out, p.sessions)
	return out
}

// Len returns the number of active sessions.
func (p *ServerPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}
