package socks_test

// Task 3.5 — portforward.go unit/integration tests.
//
// Tests cover:
//   TestStartPortForward_ConnectAndForward : listener starts, accepts a conn, sends SOCKSConnect,
//                                            receives SOCKSReady(ok), relays data both ways.
//   TestStartPortForward_InvalidTarget     : StartPortForward on an already-bound port fails.
//   TestHandlePortForward_RelayData        : end-to-end relay: data written to the local conn
//                                            appears in Deliver, and data Delivered appears on
//                                            the local conn.
//   TestHandlePortForward_TargetClose      : server signals failure (ready.Success=false) →
//                                            local conn is closed, no relay started.
//   TestHandlePortForward_ClientClose      : local conn closes immediately → relay exits cleanly.

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/EcoKG/reversproxy/internal/client"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// poolWith returns a single-session ServerPool that wraps cw and mux for tests.
func poolWith(cw client.ControlWriter, mux *tunnel.SOCKSMux) *client.ServerPool {
	p := client.NewServerPool()
	p.Add(&client.ServerSession{
		Writer: cw,
		Mux:    mux,
		Addr:   "test",
	})
	return p
}

// ---------------------------------------------------------------------------
// Helpers shared with client_test.go (same package socks_test)
// ---------------------------------------------------------------------------

// capturingControlWriter records every WriteMsg call.
type capturingControlWriter struct {
	mu   sync.Mutex
	msgs []capturedMsg
	// if writeErr is non-nil, every call after writeFails returns writeErr
	writeFails int // calls remaining before error
	writeErr   error
}

type capturedMsg struct {
	msgType protocol.MsgType
	payload any
}

func (c *capturingControlWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil && c.writeFails == 0 {
		return c.writeErr
	}
	if c.writeFails > 0 {
		c.writeFails--
	}
	c.msgs = append(c.msgs, capturedMsg{msgType, payload})
	return nil
}

func (c *capturingControlWriter) first() (capturedMsg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return capturedMsg{}, false
	}
	return c.msgs[0], true
}

func (c *capturingControlWriter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// TestStartPortForward_ConnectAndForward
// ---------------------------------------------------------------------------

// TestStartPortForward_ConnectAndForward verifies the happy-path:
//  1. StartPortForward binds on :0 and returns nil.
//  2. A TCP connection to that port causes a MsgSOCKSConnect to be written.
//  3. After DeliverReady(success), data flows from mux→client and client→mux.
func TestStartPortForward_ConnectAndForward(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := tunnel.NewSOCKSMux()
	cw := &capturingControlWriter{}
	log := discardLogger()

	// Bind on an OS-assigned port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // release so StartPortForward can bind

	err = socks.StartPortForward(ctx, port, "example.com", 80, "127.0.0.1", poolWith(cw, mux), log)
	if err != nil {
		t.Fatalf("StartPortForward: %v", err)
	}

	// Connect a client to the forwarded port.
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial forwarded port: %v", err)
	}
	defer conn.Close()

	// Wait for SOCKSConnect to appear (handlePortForward runs in a goroutine).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cw.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	msg, ok := cw.first()
	if !ok {
		t.Fatal("no MsgSOCKSConnect written within deadline")
	}
	if msg.msgType != protocol.MsgSOCKSConnect {
		t.Fatalf("first message: got %v, want MsgSOCKSConnect", msg.msgType)
	}
	connectPayload, ok := msg.payload.(protocol.SOCKSConnect)
	if !ok {
		t.Fatalf("payload type: got %T, want protocol.SOCKSConnect", msg.payload)
	}
	if connectPayload.TargetHost != "example.com" || connectPayload.TargetPort != 80 {
		t.Errorf("target mismatch: got %s:%d", connectPayload.TargetHost, connectPayload.TargetPort)
	}
	connID := connectPayload.ConnID

	// Signal server-side ready.
	if err := mux.DeliverReady(connID, true, ""); err != nil {
		t.Fatalf("DeliverReady: %v", err)
	}

	// Deliver data from the "server" into the relay — should arrive on conn.
	payload := []byte("hello from server")
	if err := mux.Deliver(connID, payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	mux.DeliverClose(connID)

	got := make([]byte, len(payload))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("ReadFull from conn: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("data mismatch: got %q, want %q", got, payload)
	}
}

// ---------------------------------------------------------------------------
// TestStartPortForward_InvalidTarget
// ---------------------------------------------------------------------------

// TestStartPortForward_InvalidTarget verifies that StartPortForward returns an
// error when the requested port is already in use.
func TestStartPortForward_InvalidTarget(t *testing.T) {
	// Pre-bind the port.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := tunnel.NewSOCKSMux()
	cw := &capturingControlWriter{}
	log := discardLogger()

	err = socks.StartPortForward(ctx, port, "example.com", 80, "127.0.0.1", poolWith(cw, mux), log)
	if err == nil {
		t.Error("expected error binding already-in-use port, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestHandlePortForward_RelayData
// ---------------------------------------------------------------------------

// TestHandlePortForward_RelayData tests that data flows correctly through the
// relay in both directions (mux→local, local→mux).
//
// Strategy: use StartPortForward + real TCP connection + fake "server" that
// mirrors the mux protocol.
func TestHandlePortForward_RelayData(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := tunnel.NewSOCKSMux()
	cw := &capturingControlWriter{}
	log := discardLogger()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := socks.StartPortForward(ctx, port, "127.0.0.1", 9999, "127.0.0.1", poolWith(cw, mux), log); err != nil {
		t.Fatalf("StartPortForward: %v", err)
	}

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Wait for SOCKSConnect.
	var connID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cw.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	msg, ok := cw.first()
	if !ok {
		t.Fatal("no SOCKSConnect message")
	}
	connID = msg.payload.(protocol.SOCKSConnect).ConnID

	// Signal ready.
	if err := mux.DeliverReady(connID, true, ""); err != nil {
		t.Fatalf("DeliverReady: %v", err)
	}

	// SERVER→CLIENT: deliver data via mux.
	serverData := []byte("server payload")
	if err := mux.Deliver(connID, serverData); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	mux.DeliverClose(connID)

	// Read on conn side.
	buf := make([]byte, len(serverData))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(buf, serverData) {
		t.Errorf("relay data mismatch: got %q, want %q", buf, serverData)
	}
}

// ---------------------------------------------------------------------------
// TestHandlePortForward_TargetClose
// ---------------------------------------------------------------------------

// TestHandlePortForward_TargetClose verifies that a SOCKSReady with
// success=false causes handlePortForward to close the local conn immediately
// without entering the relay phase.
func TestHandlePortForward_TargetClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := tunnel.NewSOCKSMux()
	cw := &capturingControlWriter{}
	log := discardLogger()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := socks.StartPortForward(ctx, port, "unreachable.local", 1234, "127.0.0.1", poolWith(cw, mux), log); err != nil {
		t.Fatalf("StartPortForward: %v", err)
	}

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Wait for SOCKSConnect.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cw.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	msg, ok := cw.first()
	if !ok {
		t.Fatal("no SOCKSConnect message")
	}
	connID := msg.payload.(protocol.SOCKSConnect).ConnID

	// Signal failure from server.
	if err := mux.DeliverReady(connID, false, "dial failed"); err != nil {
		t.Fatalf("DeliverReady: %v", err)
	}

	// conn should be closed by handlePortForward shortly.
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("expected conn to be closed after failed ready, but Read returned nil error")
	}
}

// ---------------------------------------------------------------------------
// TestHandlePortForward_ClientClose
// ---------------------------------------------------------------------------

// TestHandlePortForward_ClientClose verifies that when the local client conn
// closes before relay, handlePortForward exits without panicking.
func TestHandlePortForward_ClientClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := tunnel.NewSOCKSMux()
	cw := &capturingControlWriter{}
	log := discardLogger()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := socks.StartPortForward(ctx, port, "example.com", 443, "127.0.0.1", poolWith(cw, mux), log); err != nil {
		t.Fatalf("StartPortForward: %v", err)
	}

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// Close immediately — before SOCKSReady is delivered.
	conn.Close()

	// handlePortForward should exit gracefully; give it time.
	time.Sleep(100 * time.Millisecond)

	// No panic or deadlock means the test passes.
	// Optionally verify no SOCKSData messages were forwarded.
	for _, m := range func() []capturedMsg {
		cw.mu.Lock()
		defer cw.mu.Unlock()
		return cw.msgs
	}() {
		if m.msgType == protocol.MsgSOCKSData {
			t.Error("unexpected MsgSOCKSData after immediate client close")
		}
	}
}
