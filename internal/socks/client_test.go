package socks_test

// Tests for the CLIENT-side SOCKS5 proxy (reversed architecture).
//
// In the reversed architecture:
//
//	SOCKS5 user → Client:1080 → control tunnel → Server → Internet
//
// These tests spin up:
//  1. A "fake server" — a goroutine that reads MsgSOCKSConnect, dials the
//     target, and relays data using MsgSOCKSData / MsgSOCKSClose — mirroring
//     what control.handleServerSOCKSConnect does in production.
//  2. A CLIENT-side SOCKS5 listener via StartClientSOCKSProxy.
//  3. A local echo server as the "internet target".
//
// The test then connects to the client SOCKS5 listener, sends data, and
// verifies it echoes back correctly.

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// ControlWriter implementation for tests
// ---------------------------------------------------------------------------

type pipeControlWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (w *pipeControlWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// ---------------------------------------------------------------------------
// Fake server
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Client-side SOCKS5 integration test
// ---------------------------------------------------------------------------

// newClientTestEnv creates a pair of connected test entities:
//   - a fake server (reads/handles MsgSOCKSConnect, relays data)
//   - a client message dispatcher (reads from clientSide, delivers to clientMux)
//
// It returns the client-side control writer, the client mux, and the SOCKS
// listener address.
func newClientTestEnv(
	t *testing.T,
	ctx context.Context,
	authUser, authPass string,
) (socksAddr string, cleanup func()) {
	t.Helper()

	log := logger.New("test-client-socks")

	serverSide, clientSide := net.Pipe()

	clientMux := tunnel.NewSOCKSMux()
	serverMux := tunnel.NewSOCKSMux()

	// Fake server: processes messages from the client side.
	runFakeServerWithClientMux(t, serverSide, serverMux, clientMux)

	// Client message dispatcher: reads replies from serverSide (seen on
	// clientSide) and delivers them to clientMux — mirrors the message loop
	// in cmd/client/main.go.
	go func() {
		defer clientSide.Close()
		for {
			env, err := protocol.ReadMessage(clientSide)
			if err != nil {
				return
			}
			switch env.Type {
			case protocol.MsgSOCKSReady:
				var ready protocol.SOCKSReady
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
					continue
				}
				_ = clientMux.DeliverReady(ready.ConnID, ready.Success, ready.Error)

			case protocol.MsgSOCKSData:
				var sd protocol.SOCKSData
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
					continue
				}
				_ = clientMux.Deliver(sd.ConnID, sd.Payload)

			case protocol.MsgSOCKSClose:
				var sc protocol.SOCKSClose
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
					continue
				}
				clientMux.DeliverClose(sc.ConnID)
			}
		}
	}()

	// Start the client-side SOCKS5 proxy.
	cw := &pipeControlWriter{conn: clientSide}
	if err := socks.StartClientSOCKSProxy(ctx, "127.0.0.1:0", cw, clientMux, log, authUser, authPass); err != nil {
		t.Fatalf("StartClientSOCKSProxy: %v", err)
	}

	return socks.LastClientSOCKSAddr, func() {
		serverSide.Close()
		clientSide.Close()
	}
}

// TestClientSOCKS5_NoAuth verifies the end-to-end client-side SOCKS5 proxy
// with no authentication.
func TestClientSOCKS5_NoAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socksAddr, cleanup := newClientTestEnv(t, ctx, "", "")
	defer cleanup()

	// Start a local echo server as the "internet target".
	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	time.Sleep(30 * time.Millisecond)

	conn := dialSOCKS5NoAuth(t, socksAddr, echoHost, echoPort)
	defer conn.Close()

	msg := []byte("hello reversed socks5 no-auth")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q want %q", buf, msg)
	}
}

// TestClientSOCKS5_Auth verifies the client-side SOCKS5 proxy with RFC 1929
// username/password authentication.
func TestClientSOCKS5_Auth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const user, pass = "bob", "hunter2"
	socksAddr, cleanup := newClientTestEnv(t, ctx, user, pass)
	defer cleanup()

	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	time.Sleep(30 * time.Millisecond)

	conn := dialSOCKS5Auth(t, socksAddr, user, pass, echoHost, echoPort)
	defer conn.Close()

	msg := []byte("hello reversed socks5 with-auth")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q want %q", buf, msg)
	}
}

// TestClientSOCKS5_TargetUnreachable verifies that a CONNECT failure is
// reported back to the SOCKS5 client as a connection refused reply.
func TestClientSOCKS5_TargetUnreachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socksAddr, cleanup := newClientTestEnv(t, ctx, "", "")
	defer cleanup()

	time.Sleep(30 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5: %v", err)
	}
	defer conn.Close()

	// Greeting
	_, _ = conn.Write([]byte{0x05, 0x01, 0x00})
	choice := make([]byte, 2)
	_, _ = io.ReadFull(conn, choice)

	// CONNECT to a closed port (127.0.0.1:1).
	_ = sendConnectRequest(conn, "127.0.0.1", 1)

	replyHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, replyHdr); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if replyHdr[1] == 0x00 {
		t.Error("expected CONNECT failure, got success")
	}
}

// ---------------------------------------------------------------------------
// runFakeServerWithClientMux — full bidirectional fake server
// ---------------------------------------------------------------------------

// runFakeServerWithClientMux runs a fake server goroutine that:
//   - processes MsgSOCKSConnect (dials target, relays data via MsgSOCKSData)
//   - processes MsgSOCKSData from client (delivers to serverMux → internet target)
//   - processes MsgSOCKSClose from client
//   - delivers MsgSOCKSReady / MsgSOCKSData / MsgSOCKSClose to clientMux
//
// This is a bidirectional full simulation of the server's control.HandleControlConn
// SOCKS handling.
func runFakeServerWithClientMux(
	t *testing.T,
	serverSide net.Conn,
	serverMux *tunnel.SOCKSMux,
	clientMux *tunnel.SOCKSMux,
) {
	t.Helper()
	log := logger.New("fake-server-bidi")

	// Shared writer so all per-channel goroutines serialise writes to serverSide.
	cw := &pipeControlWriter{conn: serverSide}

	go func() {
		defer serverSide.Close()
		for {
			env, err := protocol.ReadMessage(serverSide)
			if err != nil {
				return
			}

			switch env.Type {
			case protocol.MsgSOCKSConnect:
				var sc protocol.SOCKSConnect
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
					log.Error("decode SOCKSConnect", "err", err)
					continue
				}
				// Pre-register the channel synchronously so that MsgSOCKSData
				// frames that arrive before runFakeServerChannel starts are not
				// lost.  runFakeServerChannel expects the channel to already exist.
				if _, err := serverMux.NewChannel(sc.ConnID); err != nil {
					log.Error("pre-register channel failed", "err", err)
					continue
				}
				go runFakeServerChannel(sc, cw, serverMux, log)

			case protocol.MsgSOCKSData:
				var sd protocol.SOCKSData
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
					log.Error("decode SOCKSData", "err", err)
					continue
				}
				if err := serverMux.Deliver(sd.ConnID, sd.Payload); err != nil {
					log.Debug("serverMux Deliver failed", "connID", sd.ConnID, "err", err)
				}

			case protocol.MsgSOCKSClose:
				var sc protocol.SOCKSClose
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
					log.Error("decode SOCKSClose", "err", err)
					continue
				}
				serverMux.DeliverClose(sc.ConnID)

			}
		}
	}()
}

// runFakeServerChannel dials the target and sets up a bidirectional relay
// mirroring control.handleServerSOCKSConnect.
// The channel must already be registered in sharedMux before this is called.
// cw must be a thread-safe writer shared with runFakeServerWithClientMux so
// that writes to serverSide are serialised.
func runFakeServerChannel(
	sc protocol.SOCKSConnect,
	cw *pipeControlWriter,
	sharedMux *tunnel.SOCKSMux,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
		Error(string, ...any)
		Debug(string, ...any)
	},
) {
	targetAddr := fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort)

	// The channel was pre-registered by the caller; retrieve it.
	ch := sharedMux.Get(sc.ConnID)
	if ch == nil {
		_ = cw.WriteMsg(protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: false,
			Error:   "channel not pre-registered",
		})
		return
	}
	defer sharedMux.Remove(sc.ConnID)

	targetConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		_ = cw.WriteMsg(protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: false,
			Error:   err.Error(),
		})
		return
	}
	defer targetConn.Close()

	if err := cw.WriteMsg(protocol.MsgSOCKSReady, protocol.SOCKSReady{
		ConnID:  sc.ConnID,
		Success: true,
	}); err != nil {
		return
	}

	// outSend: target → client (serialised through the mux writer)
	outSend := make(chan []byte, 64)
	muxWriterDone := make(chan struct{})

	// Goroutine A: internet target → client via MsgSOCKSData
	go func() {
		defer close(outSend)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := targetConn.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				outSend <- payload
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Goroutine B: client → internet target via ch.Recv
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = io.Copy(targetConn, ch.Recv)
		if tc, ok := targetConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// Mux writer: drains outSend → serialised MsgSOCKSData to client
	go func() {
		defer close(muxWriterDone)
		for payload := range outSend {
			if err := cw.WriteMsg(protocol.MsgSOCKSData, protocol.SOCKSData{
				ConnID:  sc.ConnID,
				Payload: payload,
			}); err != nil {
				for range outSend {
				}
				return
			}
		}
	}()

	// Wait for goroutine A to flush all data.
	<-muxWriterDone

	// Signal client: server side is done.
	_ = cw.WriteMsg(protocol.MsgSOCKSClose, protocol.SOCKSClose{ConnID: sc.ConnID})

	// Wait for goroutine B (unblocked by client's MsgSOCKSClose → DeliverClose).
	<-recvDone
}
