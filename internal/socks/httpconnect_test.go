package socks_test

// Tests for the CLIENT-side HTTP CONNECT proxy (reversed architecture).
//
// In the reversed architecture the HTTP CONNECT proxy reuses the same tunnel
// mux as the SOCKS5 proxy:
//
//	HTTP CONNECT user → Client:8080 → MsgSOCKSConnect/Data/Close → Server → Internet
//
// These tests reuse the fake-server infrastructure from client_test.go
// (runFakeServerWithClientMux / startEchoServer) and verify the HTTP CONNECT
// handshake plus bidirectional data relay.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// HTTP CONNECT test environment
// ---------------------------------------------------------------------------

// newHTTPProxyTestEnv creates a test environment with an HTTP CONNECT proxy
// backed by the same fake server used in client_test.go.  Returns the bound
// proxy address and a cleanup function.
func newHTTPProxyTestEnv(t *testing.T, ctx context.Context) (proxyAddr string, cleanup func()) {
	t.Helper()

	log := logger.New("test-http-connect")

	serverSide, clientSide := net.Pipe()

	clientMux := tunnel.NewSOCKSMux()
	serverMux := tunnel.NewSOCKSMux()

	// Fake server: handles MsgSOCKSConnect and relays data via
	// MsgSOCKSData/MsgSOCKSClose — identical to what the real server does.
	runFakeServerWithClientMux(t, serverSide, serverMux, clientMux)

	// Client message dispatcher: reads replies from serverSide (seen on
	// clientSide) and delivers them to clientMux — mirrors the message loop
	// in cmd/client/main.go.
	go func() {
		// Do NOT close clientSide here — cleanup() handles that.
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

	// Start the HTTP CONNECT proxy using clientSide as the control writer.
	cw := &pipeControlWriter{conn: clientSide}
	if err := socks.StartHTTPConnectProxy(ctx, "127.0.0.1:0", cw, clientMux, log); err != nil {
		serverSide.Close()
		clientSide.Close()
		t.Fatalf("StartHTTPConnectProxy: %v", err)
	}

	return socks.LastClientHTTPProxyAddr, func() {
		serverSide.Close()
		clientSide.Close()
	}
}

// ---------------------------------------------------------------------------
// HTTP CONNECT dial helper
// ---------------------------------------------------------------------------

// dialHTTPConnect performs an HTTP CONNECT handshake through proxyAddr to
// reach targetHost:targetPort.  Returns a usable net.Conn after the 200
// response (bufio-aware so no bytes are lost).
func dialHTTPConnect(t *testing.T, proxyAddr, targetHost string, targetPort int) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}

	target := fmt.Sprintf("%s:%d", targetHost, targetPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		t.Fatalf("write CONNECT: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		t.Fatalf("expected 200 Connection Established, got %d", resp.StatusCode)
	}

	// Wrap so buffered bytes from header parsing are not lost.
	return &bufferedConn{Conn: conn, br: br}
}

// bufferedConn wraps net.Conn + bufio.Reader so reads drain the buffer first.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (bc *bufferedConn) Read(p []byte) (int, error) {
	return bc.br.Read(p)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHTTPConnect_BasicRelay verifies the full HTTP CONNECT handshake and
// bidirectional echo relay.
func TestHTTPConnect_BasicRelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, cleanup := newHTTPProxyTestEnv(t, ctx)
	defer cleanup()

	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	time.Sleep(30 * time.Millisecond)

	conn := dialHTTPConnect(t, proxyAddr, echoHost, echoPort)
	defer conn.Close()

	msg := []byte("hello http connect proxy")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Half-close so the echo server knows we're done sending.
	if bc, ok := conn.(*bufferedConn); ok {
		if tcpConn, ok := bc.Conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q want %q", buf, msg)
	}
}

// TestHTTPConnect_NonConnectMethod verifies that non-CONNECT requests get a
// 200 OK informational response (not a tunnel).
func TestHTTPConnect_NonConnectMethod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, cleanup := newHTTPProxyTestEnv(t, ctx)
	defer cleanup()

	time.Sleep(30 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "reversproxy") {
		t.Errorf("expected informational body containing 'reversproxy', got: %q", body)
	}
}

// TestHTTPConnect_TargetUnreachable verifies that a CONNECT to an unreachable
// target results in a 502 Bad Gateway.
func TestHTTPConnect_TargetUnreachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, cleanup := newHTTPProxyTestEnv(t, ctx)
	defer cleanup()

	time.Sleep(30 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	// Port 1 is almost certainly not listening.
	target := "127.0.0.1:1"
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	_, _ = conn.Write([]byte(req))

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Error("expected non-200 status for unreachable target, got 200")
	}
}
