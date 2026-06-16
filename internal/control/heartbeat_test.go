package control_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"net"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// decodePing reads one length-prefixed frame from conn and decodes the Ping
// payload. It fails the test if reading or decoding fails.
func decodePingFrame(t *testing.T, conn net.Conn, timeout time.Duration) protocol.Ping {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("decodePingFrame: ReadMessage: %v", err)
	}
	if env.Type != protocol.MsgPing {
		t.Fatalf("decodePingFrame: expected MsgPing (%d), got %d", protocol.MsgPing, env.Type)
	}
	var p protocol.Ping
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&p); err != nil {
		t.Fatalf("decodePingFrame: decode Ping: %v", err)
	}
	return p
}

// sendPong sends a Pong reply for the given sequence number.
func sendPong(t *testing.T, conn net.Conn, seq uint64) {
	t.Helper()
	if err := protocol.WriteMessage(conn, protocol.MsgPong, protocol.Pong{Seq: seq}); err != nil {
		t.Logf("sendPong: write failed (may be expected on close): %v", err)
	}
}

// newPipeClient creates a net.Pipe()-backed *control.Client whose cancelFn is
// exposed as cancel. The client's LastHeartbeat is initialised to now.
func newPipeClient(t *testing.T) (client *control.Client, serverConn net.Conn, clientConn net.Conn, cancel context.CancelFunc) {
	t.Helper()
	serverConn, clientConn = net.Pipe()
	ctx, cancelFn := context.WithCancel(context.Background())
	_ = ctx

	reg := control.NewClientRegistry()
	c := reg.Register("test-client", "127.0.0.1:9999", serverConn, cancelFn)
	c.SetLastHeartbeat(time.Now())
	return c, serverConn, clientConn, cancelFn
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestStartHeartbeat_SendsPing verifies that StartHeartbeat writes at least
// one MsgPing frame to the connection within two tick intervals.
func TestStartHeartbeat_SendsPing(t *testing.T) {
	// Use a very short heartbeat interval by patching through the conn deadline.
	// We cannot change config.HeartbeatInterval directly, so we rely on the
	// net.Pipe read on the client side having a generous deadline.

	client, _, clientConn, cancelFn := newPipeClient(t)
	defer cancelFn()
	defer clientConn.Close()

	log := logger.New("hb-test")
	ctx, ctxCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer ctxCancel()

	// Start heartbeat in its own goroutine (as the production code does).
	go control.StartHeartbeat(ctx, client, log)

	// Wait up to 15 s for the first Ping (HeartbeatInterval = 10 s by default).
	clientConn.SetReadDeadline(time.Now().Add(15 * time.Second))
	env, err := protocol.ReadMessage(clientConn)
	if err != nil {
		t.Fatalf("expected MsgPing within 15s, got error: %v", err)
	}
	if env.Type != protocol.MsgPing {
		t.Fatalf("expected MsgPing, got type=%d", env.Type)
	}
}

// TestStartHeartbeat_PongTimeout verifies that StartHeartbeat cancels the
// client context after maxMissed unanswered pings and the timeout window.
func TestStartHeartbeat_PongTimeout(t *testing.T) {
	_, serverConn, clientConn, cancelFn := newPipeClient(t)
	_ = cancelFn

	// Track whether our cancelFn gets called by wrapping it.
	cancelCh := make(chan struct{}, 1)
	// Re-register with a cancel that signals cancelCh.
	reg2 := control.NewClientRegistry()
	outerCancel := func() {
		select {
		case cancelCh <- struct{}{}:
		default:
		}
	}
	client2 := reg2.Register("hb-timeout", "127.0.0.1:9999", serverConn, outerCancel)
	client2.SetLastHeartbeat(time.Now().Add(-60 * time.Second)) // simulate very stale heartbeat

	log := logger.New("hb-timeout-test")
	ctx, ctxCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ctxCancel()
	defer clientConn.Close()

	go control.StartHeartbeat(ctx, client2, log)

	// The heartbeat should eventually call cancelFn (our outerCancel).
	// We drain pings but never respond with pongs.
	pingCh := make(chan protocol.MsgType, 10)
	go func() {
		for {
			clientConn.SetReadDeadline(time.Now().Add(20 * time.Second))
			env, err := protocol.ReadMessage(clientConn)
			if err != nil {
				return
			}
			pingCh <- env.Type
		}
	}()

	select {
	case <-cancelCh:
		// Expected: heartbeat timed out and called cancel.
	case <-time.After(35 * time.Second):
		t.Fatal("heartbeat did not timeout within 35s")
	}
}

// TestStartHeartbeat_ContextCancel verifies that StartHeartbeat exits cleanly
// when the parent context is cancelled.
func TestStartHeartbeat_ContextCancel(t *testing.T) {
	client, _, clientConn, cancelFn := newPipeClient(t)
	defer cancelFn()
	defer clientConn.Close()

	log := logger.New("hb-ctx-cancel")
	ctx, ctxCancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		control.StartHeartbeat(ctx, client, log)
	}()

	// Cancel the context immediately.
	ctxCancel()

	select {
	case <-done:
		// StartHeartbeat returned promptly after context cancellation.
	case <-time.After(5 * time.Second):
		t.Fatal("StartHeartbeat did not exit after context cancellation within 5s")
	}
}

// TestStartHeartbeat_PongReceived verifies that when a pong is received in
// response to a ping, the client's LastHeartbeat is updated and the connection
// remains alive (heartbeat does not cancel the context prematurely).
//
// Note: heartbeat.go does not call SetLastHeartbeat itself — that is handled
// by the protocol handler that reads MsgPong. So this test focuses on the
// fact that a pong-responding peer does NOT trigger context cancellation
// within 2×HeartbeatInterval.
func TestStartHeartbeat_PongReceived(t *testing.T) {
	_, _, clientConn, cancelFn := newPipeClient(t)

	cancelCalled := make(chan struct{}, 1)
	// Replace client's cancel with one that signals cancelCalled.
	reg := control.NewClientRegistry()
	c2 := reg.Register("hb-pong", "127.0.0.1:0", clientConn, func() {
		select {
		case cancelCalled <- struct{}{}:
		default:
		}
	})
	c2.SetLastHeartbeat(time.Now())
	cancelFn() // release the original conn resources on test end
	defer clientConn.Close()

	log := logger.New("hb-pong-test")
	ctx, ctxCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ctxCancel()

	go control.StartHeartbeat(ctx, c2, log)

	// In a goroutine, respond to every Ping with a Pong and update LastHeartbeat.
	go func() {
		for {
			c2.Conn.SetReadDeadline(time.Now().Add(20 * time.Second))
			env, err := protocol.ReadMessage(c2.Conn)
			if err != nil {
				return
			}
			if env.Type == protocol.MsgPing {
				c2.SetLastHeartbeat(time.Now())
				var p protocol.Ping
				gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&p)
				protocol.WriteMessage(c2.Conn, protocol.MsgPong, protocol.Pong{Seq: p.Seq})
			}
		}
	}()

	// Assert that cancelFn is NOT called within 25s (< 3 × HeartbeatInterval).
	select {
	case <-cancelCalled:
		t.Fatal("heartbeat unexpectedly cancelled the context despite receiving pongs")
	case <-time.After(25 * time.Second):
		// Good: no unexpected cancellation.
	}
}
