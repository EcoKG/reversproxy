package tunnel

import (
	"fmt"
	"net"
	"sync"
)

// pendingConn holds the external user's net.Conn while waiting for the client
// to dial back with a matching data connection.
type pendingConn struct {
	extConn net.Conn
	ready   chan struct{} // closed when dataConn is filled
	dataConn net.Conn
}

// TunnelEntry describes a single registered tunnel.
type TunnelEntry struct {
	ID         string
	ClientID   string
	LocalHost  string
	LocalPort  int
	PublicPort int
	listener   net.Listener // the public TCP listener for this tunnel
}

// Manager tracks all active tunnels and pending data connections.
// It is safe for concurrent use.
type Manager struct {
	mu       sync.RWMutex
	tunnels  map[string]*TunnelEntry  // tunnelID → entry
	byClient map[string][]string      // clientID → []tunnelID
	pending  map[string]*pendingConn  // connID → pendingConn
}

// NewManager returns an initialised Manager.
func NewManager() *Manager {
	return &Manager{
		tunnels:  make(map[string]*TunnelEntry),
		byClient: make(map[string][]string),
		pending:  make(map[string]*pendingConn),
	}
}

// AddTunnel registers a new tunnel entry and returns it.
// The caller is responsible for setting entry.listener before external
// connections can arrive.
func (m *Manager) AddTunnel(tunnelID, clientID, localHost string, localPort, publicPort int, ln net.Listener) *TunnelEntry {
	entry := &TunnelEntry{
		ID:         tunnelID,
		ClientID:   clientID,
		LocalHost:  localHost,
		LocalPort:  localPort,
		PublicPort: publicPort,
		listener:   ln,
	}

	m.mu.Lock()
	m.tunnels[tunnelID] = entry
	m.byClient[clientID] = append(m.byClient[clientID], tunnelID)
	m.mu.Unlock()

	return entry
}

// GetTunnel returns the TunnelEntry for tunnelID, or false if not found.
func (m *Manager) GetTunnel(tunnelID string) (*TunnelEntry, bool) {
	m.mu.RLock()
	e, ok := m.tunnels[tunnelID]
	m.mu.RUnlock()
	return e, ok
}

// RemoveTunnelsForClient closes and removes all tunnels belonging to clientID.
func (m *Manager) RemoveTunnelsForClient(clientID string) {
	m.mu.Lock()
	ids := m.byClient[clientID]
	delete(m.byClient, clientID)
	for _, id := range ids {
		if e, ok := m.tunnels[id]; ok {
			if e.listener != nil {
				_ = e.listener.Close()
			}
			delete(m.tunnels, id)
		}
	}
	m.mu.Unlock()
}

// RegisterPending stores an external connection under connID and returns the
// pendingConn so the caller can wait for the data connection.
func (m *Manager) RegisterPending(connID string, extConn net.Conn) *pendingConn {
	p := &pendingConn{
		extConn: extConn,
		ready:   make(chan struct{}),
	}
	m.mu.Lock()
	m.pending[connID] = p
	m.mu.Unlock()
	return p
}

// FulfillPending matches a client data connection to the waiting external
// connection identified by connID. Returns an error if connID is unknown.
func (m *Manager) FulfillPending(connID string, dataConn net.Conn) error {
	m.mu.Lock()
	p, ok := m.pending[connID]
	if ok {
		delete(m.pending, connID)
	}
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("tunnel manager: unknown connID %q", connID)
	}

	p.dataConn = dataConn
	close(p.ready)
	return nil
}

// WaitReady blocks until the pendingConn's data connection arrives.
func WaitReady(p *pendingConn) net.Conn {
	<-p.ready
	return p.dataConn
}

// PendingExtConn returns the external user connection from a pendingConn.
func PendingExtConn(p *pendingConn) net.Conn {
	return p.extConn
}
