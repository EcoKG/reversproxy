package tunnel

import (
	"fmt"
	"io"
	"sync"
)

// SOCKSChannel is a logical, bidirectional byte-stream channel multiplexed
// through the control connection.
//
// The relay goroutine on the initiating side (CLIENT for reversed SOCKS):
//   - reads from conn → pushes frames to Send
//   - reads from Recv (data from peer) → writes to conn
//
// The mux reader on each side pushes incoming MsgSOCKSData into Recv and
// signals ReadyCh / calls Close on MsgSOCKSReady / MsgSOCKSClose.
type SOCKSChannel struct {
	ConnID string

	// ReadyCh carries exactly one result: (success bool, errMsg string).
	// It is written by the mux when a MsgSOCKSReady arrives for this channel.
	ReadyCh chan SOCKSReadyResult

	// Recv is the pipe-reader end that the relay goroutine reads from.
	// Incoming MsgSOCKSData payloads are written to the pipe-writer side.
	Recv io.Reader
	// recvW is the write side of the pipe; the mux writes here.
	recvW io.WriteCloser

	// Send is a buffered channel; the relay goroutine sends outbound payloads
	// here.  The mux writer drains it and frames them as MsgSOCKSData.
	Send chan []byte

	// done is closed once the channel is torn down.
	done chan struct{}
	once sync.Once
}

// SOCKSReadyResult carries the server's dial result for a SOCKSChannel.
type SOCKSReadyResult struct {
	Success bool
	ErrMsg  string
}

// Close marks the channel done.  Idempotent.
func (c *SOCKSChannel) Close() {
	c.once.Do(func() { close(c.done) })
}

// Done returns a channel that is closed when this SOCKS channel has ended.
func (c *SOCKSChannel) Done() <-chan struct{} {
	return c.done
}

// SOCKSMux manages all active multiplexed SOCKS channels for one endpoint.
// It is safe for concurrent use.
type SOCKSMux struct {
	mu       sync.RWMutex
	channels map[string]*SOCKSChannel
}

// NewSOCKSMux returns an initialised, empty mux.
func NewSOCKSMux() *SOCKSMux {
	return &SOCKSMux{channels: make(map[string]*SOCKSChannel)}
}

// NewChannel allocates a SOCKSChannel for connID and registers it.
// Returns an error if connID is already registered.
func (m *SOCKSMux) NewChannel(connID string) (*SOCKSChannel, error) {
	pr, pw := io.Pipe()
	ch := &SOCKSChannel{
		ConnID:  connID,
		ReadyCh: make(chan SOCKSReadyResult, 1),
		Recv:    pr,
		recvW:   pw,
		Send:    make(chan []byte, 64),
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.channels[connID]; exists {
		_ = pw.Close()
		return nil, fmt.Errorf("socks_mux: channel %q already registered", connID)
	}

	m.channels[connID] = ch
	return ch, nil
}

// Remove deregisters and closes the channel for connID.  No-op if not found.
func (m *SOCKSMux) Remove(connID string) {
	m.mu.Lock()
	ch, ok := m.channels[connID]
	if ok {
		delete(m.channels, connID)
	}
	m.mu.Unlock()

	if ok {
		ch.Close()
		_ = ch.recvW.Close()
	}
}

// Deliver pushes data into the Recv pipe for the channel identified by connID.
// Returns an error if connID is unknown or the pipe is broken.
func (m *SOCKSMux) Deliver(connID string, data []byte) error {
	m.mu.RLock()
	ch, ok := m.channels[connID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("socks_mux: unknown connID %q", connID)
	}

	_, err := ch.recvW.Write(data)
	return err
}

// DeliverReady signals a SOCKSReady result to the waiting channel.
// Returns an error if connID is unknown.
func (m *SOCKSMux) DeliverReady(connID string, success bool, errMsg string) error {
	m.mu.RLock()
	ch, ok := m.channels[connID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("socks_mux: unknown connID %q for ready", connID)
	}

	select {
	case ch.ReadyCh <- SOCKSReadyResult{Success: success, ErrMsg: errMsg}:
	default:
		// Already delivered (shouldn't happen in practice).
	}
	return nil
}

// DeliverClose signals that the peer has closed its side of the channel.
// It removes the channel from the registry and closes the Recv pipe.
func (m *SOCKSMux) DeliverClose(connID string) {
	m.mu.Lock()
	ch, ok := m.channels[connID]
	if ok {
		delete(m.channels, connID)
	}
	m.mu.Unlock()

	if ok {
		ch.Close()
		_ = ch.recvW.Close() // unblocks any pending Recv read with EOF
	}
}

// Get returns the channel for connID, or nil if not found.
func (m *SOCKSMux) Get(connID string) *SOCKSChannel {
	m.mu.RLock()
	ch := m.channels[connID]
	m.mu.RUnlock()
	return ch
}

// CloseAll tears down every registered channel (e.g. on control conn loss).
func (m *SOCKSMux) CloseAll() {
	m.mu.Lock()
	chs := make([]*SOCKSChannel, 0, len(m.channels))
	for _, ch := range m.channels {
		chs = append(chs, ch)
	}
	m.channels = make(map[string]*SOCKSChannel)
	m.mu.Unlock()

	for _, ch := range chs {
		ch.Close()
		_ = ch.recvW.Close()
	}
}
