package tunnel

import (
	"net"
	"sync"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

// CtrlConnWriter serialises ALL protocol writes to a single control connection.
// protocol.WriteMessage performs two non-atomic writes (a length prefix then the
// body), so without serialisation concurrent writers — the heartbeat, the
// HTTP/HTTPS/TCP proxy OpenConnection sends, the control-loop responses, and the
// SOCKS relay frames — could interleave one frame's prefix with another's body
// and corrupt the stream. Every write to a control conn must go through the one
// CtrlConnWriter that owns it. It is safe for concurrent use.
type CtrlConnWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

// NewCtrlConnWriter returns a serialising writer for conn.
func NewCtrlConnWriter(conn net.Conn) *CtrlConnWriter {
	return &CtrlConnWriter{conn: conn}
}

// Write frames and writes a single protocol message, holding the lock for the
// whole (length-prefix + body) write so frames are atomic relative to one another.
func (w *CtrlConnWriter) Write(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// Conn returns the underlying connection for deadline/close operations (which
// do not need the write lock).
func (w *CtrlConnWriter) Conn() net.Conn { return w.conn }

// ControlConnRegistry maps clientID → the serialising CtrlConnWriter for that
// client's TLS control connection. The HTTP/HTTPS proxy uses it to send
// OpenConnection messages to the correct client without racing other writers.
//
// It is safe for concurrent use.
type ControlConnRegistry struct {
	mu      sync.RWMutex
	writers map[string]*CtrlConnWriter
}

// NewControlConnRegistry returns an empty, initialised ControlConnRegistry.
func NewControlConnRegistry() *ControlConnRegistry {
	return &ControlConnRegistry{
		writers: make(map[string]*CtrlConnWriter),
	}
}

// Register associates clientID with its control-connection writer.
// Any previous registration for clientID is silently overwritten.
func (r *ControlConnRegistry) Register(clientID string, w *CtrlConnWriter) {
	r.mu.Lock()
	r.writers[clientID] = w
	r.mu.Unlock()
}

// Deregister removes the entry for clientID.
func (r *ControlConnRegistry) Deregister(clientID string) {
	r.mu.Lock()
	delete(r.writers, clientID)
	r.mu.Unlock()
}

// Get returns the control-connection writer for clientID, or (nil, false).
func (r *ControlConnRegistry) Get(clientID string) (*CtrlConnWriter, bool) {
	r.mu.RLock()
	w, ok := r.writers[clientID]
	r.mu.RUnlock()
	return w, ok
}

// PickAny returns the writer of an arbitrary connected client, or (nil, false)
// if none are connected.
func (r *ControlConnRegistry) PickAny() (*CtrlConnWriter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, w := range r.writers {
		return w, true
	}
	return nil, false
}
