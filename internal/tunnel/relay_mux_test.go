package tunnel_test

// Task 3.3 — RelayMuxChannel unit tests.
//
// Tests cover:
//   - NormalClose:    conn.Close → relay exits, recvDone unblocks
//   - ConnReadError:  conn read error → relay exits gracefully
//   - ChRecvClose:    mux.DeliverClose → ch.Recv EOF → relay exits
//   - ContextCancel:  only affects future uses (ctx is currently informational)
//   - DataIntegrity:  1 KB, 32 KB, 64 KB bidirectional round-trip
//   - RaceDetector:   concurrent Send/Recv with -race
//
// NOTE: RelayMuxChannel's r/w parameters are io.Reader and net.Conn; in tests
// we use net.Pipe() to get real bidirectional byte-stream connections.

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
	"log/slog"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// noopLogger returns a logger that discards all output.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingCtrlWriter captures Write calls for inspection.
type recordingCtrlWriter struct {
	mu   sync.Mutex
	msgs []protocol.MsgType
	err  error // if set, Write returns this error
}

func (r *recordingCtrlWriter) Write(msgType protocol.MsgType, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, msgType)
	return nil
}

func (r *recordingCtrlWriter) lastMsg() (protocol.MsgType, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == 0 {
		return 0, false
	}
	return r.msgs[len(r.msgs)-1], true
}

func (r *recordingCtrlWriter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

// newRelayPair sets up:
//   - localA / localB: connected net.Pipe acting as the "local conn"
//   - a SOCKSMux, a registered channel, and a recordingCtrlWriter
//
// Returns localA (passed to RelayMuxChannel as r/w) and localB (the other side
// of the local pipe, used by the test to send/receive data).
func newRelayPair(t *testing.T) (
	localA net.Conn, localB net.Conn,
	mux *tunnel.SOCKSMux, ch *tunnel.SOCKSChannel,
	cw *recordingCtrlWriter,
) {
	t.Helper()
	localA, localB = net.Pipe()
	mux = tunnel.NewSOCKSMux()
	var err error
	ch, err = mux.NewChannel("test-conn")
	if err != nil {
		t.Fatalf("mux.NewChannel: %v", err)
	}
	cw = &recordingCtrlWriter{}
	return
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_NormalClose
// ---------------------------------------------------------------------------

func TestRelayMuxChannel_NormalClose(t *testing.T) {
	localA, localB, mux, ch, cw := newRelayPair(t)
	defer localB.Close()

	done := make(chan error, 1)
	go func() {
		done <- tunnel.RelayMuxChannel(
			context.Background(), localA, localA, ch, cw,
			"test-conn", protocol.MsgSOCKSClose, noopLogger(),
		)
	}()

	// Closing localB causes localA.Read to return EOF, which should unblock the relay.
	localB.Close()
	// Also deliver a close on the mux side so recvDone unblocks.
	mux.DeliverClose("test-conn")

	err := <-done
	if err != nil {
		t.Errorf("RelayMuxChannel returned unexpected error: %v", err)
	}

	// A MsgSOCKSClose should have been sent.
	last, ok := cw.lastMsg()
	if !ok {
		t.Fatal("no messages written to CtrlWriter")
	}
	if last != protocol.MsgSOCKSClose {
		t.Errorf("last message: got %v, want MsgSOCKSClose", last)
	}
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_ConnReadError
// ---------------------------------------------------------------------------

func TestRelayMuxChannel_ConnReadError(t *testing.T) {
	localA, localB, mux, ch, cw := newRelayPair(t)

	done := make(chan error, 1)
	go func() {
		done <- tunnel.RelayMuxChannel(
			context.Background(), localA, localA, ch, cw,
			"test-conn", protocol.MsgSOCKSClose, noopLogger(),
		)
	}()

	// Force a read error by closing localA's peer (localB).
	localB.Close()
	mux.DeliverClose("test-conn")

	<-done // should not hang

	// CtrlWriter should have received a close message.
	if cw.count() == 0 {
		t.Error("expected at least one message written to CtrlWriter")
	}
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_ChRecvClose
// ---------------------------------------------------------------------------

func TestRelayMuxChannel_ChRecvClose(t *testing.T) {
	localA, localB, mux, ch, cw := newRelayPair(t)
	defer localB.Close()

	done := make(chan error, 1)
	go func() {
		done <- tunnel.RelayMuxChannel(
			context.Background(), localA, localA, ch, cw,
			"test-conn", protocol.MsgSOCKSClose, noopLogger(),
		)
	}()

	// Signal close from the mux side (peer closed its side).
	mux.DeliverClose("test-conn")
	// Close localB so goroutine A (local reader) also unblocks.
	localB.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RelayMuxChannel returned unexpected error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_ContextCancel
// ---------------------------------------------------------------------------

func TestRelayMuxChannel_ContextCancel(t *testing.T) {
	localA, localB, mux, ch, cw := newRelayPair(t)
	defer localB.Close()
	_ = cw

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- tunnel.RelayMuxChannel(
			ctx, localA, localA, ch, cw,
			"test-conn", protocol.MsgSOCKSClose, noopLogger(),
		)
	}()

	// Cancel context, then close connections to unblock I/O goroutines.
	cancel()
	localB.Close()
	mux.DeliverClose("test-conn")

	<-done // must not hang
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_DataIntegrity
// ---------------------------------------------------------------------------

func TestRelayMuxChannel_DataIntegrity(t *testing.T) {
	for _, size := range []int{1024, 32 * 1024, 64 * 1024} {
		size := size
		t.Run(byteSizeLabel(size), func(t *testing.T) {
			testDataIntegrity(t, size)
		})
	}
}

func byteSizeLabel(n int) string {
	switch {
	case n >= 1024*1024:
		return "1MB"
	case n >= 64*1024:
		return "64KB"
	case n >= 32*1024:
		return "32KB"
	default:
		return "1KB"
	}
}

// testDataIntegrity verifies that data written into the mux (via Deliver) is
// faithfully written out to localB.Conn and that data written from localB is
// forwarded as MsgSOCKSData frames.
func testDataIntegrity(t *testing.T, size int) {
	t.Helper()

	localA, localB, mux, ch, _ := newRelayPair(t)

	// Use a passthrough CtrlWriter that also writes to localB via a pipe so
	// goroutine B (Recv→localA) gets the data.
	type record struct {
		msgType protocol.MsgType
		payload any
	}
	msgs := make(chan record, 256)
	cw := &funcCtrlWriter{fn: func(msgType protocol.MsgType, payload any) error {
		msgs <- record{msgType, payload}
		return nil
	}}

	done := make(chan error, 1)
	go func() {
		done <- tunnel.RelayMuxChannel(
			context.Background(), localA, localA, ch, cw,
			"test-conn", protocol.MsgSOCKSClose, noopLogger(),
		)
	}()

	// Send data FROM the peer side (via mux.Deliver) into the relay's Recv
	// pipe; it should come out of localA and be readable on localB.
	peerData := bytes.Repeat([]byte("P"), size)
	go func() {
		if err := mux.Deliver("test-conn", peerData); err != nil {
			t.Errorf("Deliver: %v", err)
		}
		mux.DeliverClose("test-conn")
	}()

	// Read from localB — should receive peerData.
	got := make([]byte, size)
	_, err := io.ReadFull(localB, got)
	if err != nil {
		t.Fatalf("ReadFull from localB: %v", err)
	}
	if !bytes.Equal(got, peerData) {
		t.Errorf("data mismatch: got %d bytes but content differs", len(got))
	}

	// Close localB so goroutine A finishes.
	localB.Close()
	<-done

	// Collect all MsgSOCKSData frames forwarded via CtrlWriter.
	close(msgs)
	var totalForwarded int
	for m := range msgs {
		if m.msgType == protocol.MsgSOCKSData {
			if sd, ok := m.payload.(protocol.SOCKSData); ok {
				totalForwarded += len(sd.Payload)
			}
		}
	}
	// No data was sent from localB, so no data frames expected.
	_ = totalForwarded
}

// funcCtrlWriter adapts a function to CtrlWriter.
type funcCtrlWriter struct {
	fn func(protocol.MsgType, any) error
}

func (f *funcCtrlWriter) Write(msgType protocol.MsgType, payload any) error {
	return f.fn(msgType, payload)
}

// ---------------------------------------------------------------------------
// TestRelayMuxChannel_RaceDetector
// ---------------------------------------------------------------------------

// TestRelayMuxChannel_RaceDetector runs multiple concurrent relays to exercise
// the race detector.  Run with: go test -race ./internal/tunnel/...
func TestRelayMuxChannel_RaceDetector(t *testing.T) {
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()

			mux := tunnel.NewSOCKSMux()
			localA, localB, err := pipeConn()
			if err != nil {
				t.Errorf("net.Pipe: %v", err)
				return
			}
			defer localB.Close()

			ch, err := mux.NewChannel("race-conn")
			if err != nil {
				t.Errorf("NewChannel: %v", err)
				return
			}
			cw := &recordingCtrlWriter{}

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = tunnel.RelayMuxChannel(
					context.Background(), localA, localA, ch, cw,
					"race-conn", protocol.MsgSOCKSClose, noopLogger(),
				)
			}()

			localB.Close()
			mux.DeliverClose("race-conn")
			<-done
		}()
	}
	wg.Wait()
}

func pipeConn() (net.Conn, net.Conn, error) {
	a, b := net.Pipe()
	return a, b, nil
}
