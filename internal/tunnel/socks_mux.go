package tunnel

import (
	"fmt"
	"io"
	"sync"
	"time"
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
	// recvW is the write side of the pipe; the drain goroutine writes here.
	recvW io.WriteCloser
	// recvQ buffers inbound payloads between Deliver (called from the shared
	// control read loop) and the per-channel drain goroutine, so that a slow
	// local consumer can never block the shared loop (head-of-line blocking).
	recvQ chan []byte

	// Send is a buffered channel; the relay goroutine sends outbound payloads
	// here.  The mux writer drains it and frames them as MsgSOCKSData.
	Send chan []byte

	// done is closed once the channel is torn down.
	done chan struct{}
	once sync.Once

	// recvEOF is closed by a graceful peer-close (DeliverClose) to tell the
	// drain goroutine to flush any buffered payloads and then EOF the pipe, so
	// no inbound bytes are lost ahead of the close. A forced teardown closes
	// `done` instead, which drops buffered payloads.
	recvEOF     chan struct{}
	recvEOFOnce sync.Once
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

// CloseRecv forces the receive side down: it wakes the drain goroutine and
// EOFs the pipe so a goroutine blocked reading c.Recv (the relay's peer→local
// copy) unblocks. Buffered-but-undelivered payloads are dropped. Idempotent.
func (c *SOCKSChannel) CloseRecv() {
	c.Close()           // wake drainRecv via the done case
	_ = c.recvW.Close() // EOF the pipe promptly even if drainRecv lags
}

// signalRecvEOF marks a graceful end of inbound data. Idempotent.
func (c *SOCKSChannel) signalRecvEOF() {
	c.recvEOFOnce.Do(func() { close(c.recvEOF) })
}

// drainRecv moves buffered inbound payloads from recvQ into the pipe,
// decoupling the (blocking) pipe write from Deliver so a slow local consumer
// can never stall the shared control read loop. On a graceful close (recvEOF)
// it flushes the remaining buffer before EOFing the pipe; on a forced teardown
// (done) it drops the buffer. It always EOFs the pipe on exit.
func (c *SOCKSChannel) drainRecv() {
	defer func() { _ = c.recvW.Close() }()
	for {
		select {
		case data := <-c.recvQ:
			if _, err := c.recvW.Write(data); err != nil {
				return
			}
		case <-c.recvEOF:
			c.flushRecvQ()
			return
		case <-c.done:
			// done may be closed alongside recvEOF (graceful path); if so, still
			// flush so ordered close does not drop buffered payloads.
			select {
			case <-c.recvEOF:
				c.flushRecvQ()
			default:
			}
			return
		}
	}
}

// flushRecvQ writes every currently-buffered payload to the pipe without
// blocking on new arrivals. A wedged reader makes recvW.Write return an error,
// which stops the flush.
func (c *SOCKSChannel) flushRecvQ() {
	for {
		select {
		case data := <-c.recvQ:
			if _, err := c.recvW.Write(data); err != nil {
				return
			}
		default:
			return
		}
	}
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
	wg       sync.WaitGroup
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
		recvQ:   make(chan []byte, 64),
		Send:    make(chan []byte, 64),
		done:    make(chan struct{}),
		recvEOF: make(chan struct{}),
	}

	m.mu.Lock()
	if _, exists := m.channels[connID]; exists {
		m.mu.Unlock()
		_ = pw.Close()
		return nil, fmt.Errorf("socks_mux: channel %q already registered", connID)
	}

	m.channels[connID] = ch
	m.wg.Add(1)
	m.mu.Unlock()

	go ch.drainRecv()
	return ch, nil
}

// teardown deregisters connID and tears down its channel. When graceful is
// true (peer-initiated close) any buffered inbound payloads are flushed before
// the pipe is EOF'd; when false (forced) they are dropped and the pipe is
// closed immediately. Returns false if connID was not registered.
func (m *SOCKSMux) teardown(connID string, graceful bool) bool {
	m.mu.Lock()
	ch, ok := m.channels[connID]
	if ok {
		delete(m.channels, connID)
	}
	m.mu.Unlock()

	if !ok {
		return false
	}
	if graceful {
		ch.signalRecvEOF() // drainRecv flushes buffered payloads, then EOFs the pipe
		ch.Close()
	} else {
		ch.Close()
		_ = ch.recvW.Close() // drop buffered; EOF the pipe immediately
	}
	m.wg.Done()
	return true
}

// Remove deregisters and force-closes the channel for connID.  No-op if not
// found. Used for relay-side cleanup, where inbound data has already drained.
func (m *SOCKSMux) Remove(connID string) {
	m.teardown(connID, false)
}

// Deliver enqueues data for the channel identified by connID. It never blocks
// the caller (the shared control read loop): payloads are buffered in the
// channel's bounded recvQ and moved to the pipe by a dedicated drain goroutine.
// If the buffer is full the local consumer is wedged, so the single channel is
// torn down rather than stalling every other channel and the heartbeat.
// Returns an error if connID is unknown, already closed, or was dropped.
func (m *SOCKSMux) Deliver(connID string, data []byte) error {
	m.mu.RLock()
	ch, ok := m.channels[connID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("socks_mux: unknown connID %q", connID)
	}

	select {
	case ch.recvQ <- data:
		return nil
	case <-ch.done:
		return fmt.Errorf("socks_mux: channel %q closed", connID)
	default:
		// recvQ full → slow/wedged consumer. Force-drop this single channel so
		// the shared control read loop is never blocked.
		m.teardown(connID, false)
		return fmt.Errorf("socks_mux: recv buffer full for %q; channel dropped", connID)
	}
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

// DeliverClose signals that the peer has gracefully closed its side of the
// channel. Any inbound payloads still buffered are flushed to the Recv pipe
// before it is EOF'd, so no bytes are lost ahead of the close.
func (m *SOCKSMux) DeliverClose(connID string) {
	m.teardown(connID, true)
}

// Get returns the channel for connID, or nil if not found.
func (m *SOCKSMux) Get(connID string) *SOCKSChannel {
	m.mu.RLock()
	ch := m.channels[connID]
	m.mu.RUnlock()
	return ch
}

// DrainAndClose waits up to timeout for all channels to finish, then
// force-closes any that remain. This allows in-flight relays to finish
// gracefully before a hard teardown. It uses a WaitGroup instead of polling.
func (m *SOCKSMux) DrainAndClose(timeout time.Duration) {
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		m.CloseAll()
	}
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
		m.wg.Done()
	}
}
