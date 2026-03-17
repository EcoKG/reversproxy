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

// HTTPTunnelEntry describes an HTTP or HTTPS tunnel registered by a client.
type HTTPTunnelEntry struct {
	ID        string
	ClientID  string
	Hostname  string
	LocalHost string
	LocalPort int
	IsTLS     bool // true for HTTPS/SNI tunnels, false for plain HTTP
	// CtrlConn is the control connection to the owning client, used to send
	// OpenConnection messages when a matching HTTP/HTTPS request arrives.
	CtrlConn interface{ Write([]byte) (int, error) }
}

// Manager tracks all active tunnels and pending data connections.
// It is safe for concurrent use.
type Manager struct {
	mu          sync.RWMutex
	tunnels     map[string]*TunnelEntry     // tunnelID → entry
	byClient    map[string][]string         // clientID → []tunnelID
	pending     map[string]*pendingConn     // connID → pendingConn
	httpTunnels map[string]*HTTPTunnelEntry // hostname → HTTPTunnelEntry (plain HTTP)
	httpsTunnels map[string]*HTTPTunnelEntry // hostname → HTTPTunnelEntry (HTTPS/SNI)
	httpByClient map[string][]string        // clientID → []hostname (HTTP)
	httpsByClient map[string][]string       // clientID → []hostname (HTTPS)
}

// NewManager returns an initialised Manager.
func NewManager() *Manager {
	return &Manager{
		tunnels:       make(map[string]*TunnelEntry),
		byClient:      make(map[string][]string),
		pending:       make(map[string]*pendingConn),
		httpTunnels:   make(map[string]*HTTPTunnelEntry),
		httpsTunnels:  make(map[string]*HTTPTunnelEntry),
		httpByClient:  make(map[string][]string),
		httpsByClient: make(map[string][]string),
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

// AddHTTPTunnel registers a hostname for plain-HTTP routing.
// Returns the new entry. If the hostname is already registered the old entry is replaced.
func (m *Manager) AddHTTPTunnel(tunnelID, clientID, hostname, localHost string, localPort int) *HTTPTunnelEntry {
	entry := &HTTPTunnelEntry{
		ID:        tunnelID,
		ClientID:  clientID,
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
		IsTLS:     false,
	}
	m.mu.Lock()
	m.httpTunnels[hostname] = entry
	m.httpByClient[clientID] = append(m.httpByClient[clientID], hostname)
	m.mu.Unlock()
	return entry
}

// AddHTTPSTunnel registers a hostname for HTTPS/SNI routing.
func (m *Manager) AddHTTPSTunnel(tunnelID, clientID, hostname, localHost string, localPort int) *HTTPTunnelEntry {
	entry := &HTTPTunnelEntry{
		ID:        tunnelID,
		ClientID:  clientID,
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
		IsTLS:     true,
	}
	m.mu.Lock()
	m.httpsTunnels[hostname] = entry
	m.httpsByClient[clientID] = append(m.httpsByClient[clientID], hostname)
	m.mu.Unlock()
	return entry
}

// GetHTTPTunnel looks up an HTTP tunnel by hostname. Returns nil, false if not found.
func (m *Manager) GetHTTPTunnel(hostname string) (*HTTPTunnelEntry, bool) {
	m.mu.RLock()
	e, ok := m.httpTunnels[hostname]
	m.mu.RUnlock()
	return e, ok
}

// GetHTTPSTunnel looks up an HTTPS tunnel by SNI hostname. Returns nil, false if not found.
func (m *Manager) GetHTTPSTunnel(hostname string) (*HTTPTunnelEntry, bool) {
	m.mu.RLock()
	e, ok := m.httpsTunnels[hostname]
	m.mu.RUnlock()
	return e, ok
}

// ListTunnels returns a snapshot of all currently registered TCP tunnels.
func (m *Manager) ListTunnels() []*TunnelEntry {
	m.mu.RLock()
	out := make([]*TunnelEntry, 0, len(m.tunnels))
	for _, e := range m.tunnels {
		out = append(out, e)
	}
	m.mu.RUnlock()
	return out
}

// ListHTTPTunnels returns a snapshot of all registered HTTP and HTTPS tunnels.
func (m *Manager) ListHTTPTunnels() []*HTTPTunnelEntry {
	m.mu.RLock()
	out := make([]*HTTPTunnelEntry, 0, len(m.httpTunnels)+len(m.httpsTunnels))
	for _, e := range m.httpTunnels {
		out = append(out, e)
	}
	for _, e := range m.httpsTunnels {
		out = append(out, e)
	}
	m.mu.RUnlock()
	return out
}

// RemoveHTTPTunnelsForClient removes all HTTP and HTTPS hostname registrations
// belonging to clientID.
func (m *Manager) RemoveHTTPTunnelsForClient(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, h := range m.httpByClient[clientID] {
		delete(m.httpTunnels, h)
	}
	delete(m.httpByClient, clientID)

	for _, h := range m.httpsByClient[clientID] {
		delete(m.httpsTunnels, h)
	}
	delete(m.httpsByClient, clientID)
}
