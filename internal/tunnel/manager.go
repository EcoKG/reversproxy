package tunnel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
)

// pendingConn holds the external user's net.Conn while waiting for the client
// to dial back with a matching data connection.
type pendingConn struct {
	extConn   net.Conn
	ready     chan struct{} // closed (exactly once) when the wait resolves
	dataConn  net.Conn
	closeOnce sync.Once // guards close(ready) so Fulfill/Cancel cannot double-close
	// dataToken is a per-connection single-use secret sent to the legitimate
	// client (over the authenticated control channel) inside OpenConnection. The
	// client must echo it in DataConnHello, so knowledge of the public connID
	// alone is insufficient to fulfil (hijack) a pending data connection.
	dataToken string
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
	mu            sync.RWMutex
	tunnels       map[string]*TunnelEntry     // tunnelID → entry
	byClient      map[string][]string         // clientID → []tunnelID
	pending       map[string]*pendingConn     // connID → pendingConn
	httpTunnels   map[string]*HTTPTunnelEntry // hostname → HTTPTunnelEntry (plain HTTP)
	httpsTunnels  map[string]*HTTPTunnelEntry // hostname → HTTPTunnelEntry (HTTPS/SNI)
	httpByClient  map[string][]string         // clientID → []hostname (HTTP)
	httpsByClient map[string][]string         // clientID → []hostname (HTTPS)
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
// pendingConn so the caller can wait for the data connection. A single-use data
// token is generated and must be echoed by the client in DataConnHello.
func (m *Manager) RegisterPending(connID string, extConn net.Conn) *pendingConn {
	p := &pendingConn{
		extConn:   extConn,
		ready:     make(chan struct{}),
		dataToken: newDataToken(),
	}
	m.mu.Lock()
	m.pending[connID] = p
	m.mu.Unlock()
	return p
}

// newDataToken returns a 256-bit cryptographically-random hex token. On the
// (practically impossible) failure of crypto/rand it returns "" — FulfillPending
// then rejects all data conns for that pending entry (fail closed).
func newDataToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// PendingToken returns the single-use data token for a pendingConn. Callers
// place it in the OpenConnection message sent to the owning client.
func PendingToken(p *pendingConn) string {
	return p.dataToken
}

// CancelPending removes a pending connection entry without fulfilling it and
// closes its ready channel so any goroutine parked in WaitReady unblocks
// (returning a nil dataConn). This cleans up the map entry AND the waiter when
// the external conn is closed before the client dials back (e.g. context
// cancelled, timeout, OpenConnection send failure).
func (m *Manager) CancelPending(connID string) {
	m.mu.Lock()
	p, ok := m.pending[connID]
	if ok {
		delete(m.pending, connID)
	}
	m.mu.Unlock()
	if ok {
		p.closeOnce.Do(func() { close(p.ready) })
	}
}

// FulfillPending matches a client data connection to the waiting external
// connection identified by connID. The presented token must match the
// single-use token issued at RegisterPending (constant-time), otherwise the
// data conn is rejected WITHOUT consuming the pending entry — so a wrong guess
// cannot cancel the legitimate fulfilment. Returns an error if connID is
// unknown or the token is invalid.
func (m *Manager) FulfillPending(connID string, dataConn net.Conn, token string) error {
	m.mu.Lock()
	p, ok := m.pending[connID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel manager: unknown connID %q", connID)
	}
	if p.dataToken == "" || subtle.ConstantTimeCompare([]byte(p.dataToken), []byte(token)) != 1 {
		m.mu.Unlock()
		return fmt.Errorf("tunnel manager: invalid data token for connID %q", connID)
	}
	delete(m.pending, connID)
	m.mu.Unlock()

	p.dataConn = dataConn
	p.closeOnce.Do(func() { close(p.ready) })
	return nil
}

// WaitReady blocks until the pendingConn's data connection arrives, or until
// the pending entry is cancelled (in which case it returns nil).
func WaitReady(p *pendingConn) net.Conn {
	<-p.ready
	return p.dataConn
}

// PendingExtConn returns the external user connection from a pendingConn.
func PendingExtConn(p *pendingConn) net.Conn {
	return p.extConn
}

// AddHTTPTunnel registers a hostname for plain-HTTP routing.
// Returns an error if the hostname is already registered by a different client.
// If the same client re-registers a hostname, the old entry is replaced without
// duplicating the hostname in httpByClient.
func (m *Manager) AddHTTPTunnel(tunnelID, clientID, hostname, localHost string, localPort int) (*HTTPTunnelEntry, error) {
	entry := &HTTPTunnelEntry{
		ID:        tunnelID,
		ClientID:  clientID,
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
		IsTLS:     false,
	}
	m.mu.Lock()
	if existing, ok := m.httpTunnels[hostname]; ok && existing.ClientID != clientID {
		m.mu.Unlock()
		return nil, fmt.Errorf("hostname %q already registered by client %s", hostname, existing.ClientID)
	}
	// Only append to the per-client list if this hostname is not already tracked.
	alreadyTracked := false
	for _, h := range m.httpByClient[clientID] {
		if h == hostname {
			alreadyTracked = true
			break
		}
	}
	if !alreadyTracked {
		m.httpByClient[clientID] = append(m.httpByClient[clientID], hostname)
	}
	m.httpTunnels[hostname] = entry
	m.mu.Unlock()
	return entry, nil
}

// AddHTTPSTunnel registers a hostname for HTTPS/SNI routing.
// Returns an error if the hostname is already registered by a different client.
// If the same client re-registers a hostname, the old entry is replaced without
// duplicating the hostname in httpsByClient.
func (m *Manager) AddHTTPSTunnel(tunnelID, clientID, hostname, localHost string, localPort int) (*HTTPTunnelEntry, error) {
	entry := &HTTPTunnelEntry{
		ID:        tunnelID,
		ClientID:  clientID,
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
		IsTLS:     true,
	}
	m.mu.Lock()
	if existing, ok := m.httpsTunnels[hostname]; ok && existing.ClientID != clientID {
		m.mu.Unlock()
		return nil, fmt.Errorf("HTTPS hostname %q already registered by client %s", hostname, existing.ClientID)
	}
	// Only append to the per-client list if this hostname is not already tracked.
	alreadyTracked := false
	for _, h := range m.httpsByClient[clientID] {
		if h == hostname {
			alreadyTracked = true
			break
		}
	}
	if !alreadyTracked {
		m.httpsByClient[clientID] = append(m.httpsByClient[clientID], hostname)
	}
	m.httpsTunnels[hostname] = entry
	m.mu.Unlock()
	return entry, nil
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
