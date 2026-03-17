package socks_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

// testEnv holds all the in-process components for a SOCKS5 integration test.
type testEnv struct {
	mgr        *tunnel.Manager
	ctrlConns  *tunnel.ControlConnRegistry
	dataAddr   string
	socksAddr  string
	cancel     context.CancelFunc
}

// startTestEnv creates:
//   - A data listener (server side).
//   - A SOCKS5 proxy (server side) on a random port.
//   - An in-process "client" that handles MsgSOCKSConnect messages.
//
// The returned cleanup function shuts everything down.
func startTestEnv(t *testing.T, authUser, authPass string) (*testEnv, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	mgr := tunnel.NewManager()
	ctrlConns := tunnel.NewControlConnRegistry()
	log := logger.New("test-socks")

	// Start the data listener.
	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", mgr, log); err != nil {
		cancel()
		t.Fatalf("StartDataListener: %v", err)
	}
	dataAddr := tunnel.DataAddr

	// Start the SOCKS5 proxy.
	if err := socks.StartSOCKSProxy(ctx, "127.0.0.1:0", mgr, ctrlConns, dataAddr, log, authUser, authPass); err != nil {
		cancel()
		t.Fatalf("StartSOCKSProxy: %v", err)
	}
	socksAddr := socks.LastSOCKSAddr

	env := &testEnv{
		mgr:       mgr,
		ctrlConns: ctrlConns,
		dataAddr:  dataAddr,
		socksAddr: socksAddr,
		cancel:    cancel,
	}

	return env, func() { cancel() }
}

// connectFakeClient creates an in-process "fake client" that:
//  1. Registers its control conn in ctrlConns (server side of the pipe).
//  2. Runs a server-side reader that processes MsgSOCKSReady by calling
//     mgr.FulfillSOCKS — mirroring what HandleControlConn does.
//  3. Handles MsgSOCKSConnect messages (client side) by dialing the target,
//     opening a data connection back to the server, and sending MsgSOCKSReady.
func connectFakeClient(t *testing.T, env *testEnv) (cleanup func()) {
	t.Helper()

	log := logger.New("fake-client")

	// Create a pair of in-process net.Pipe connections that simulate the
	// control channel between the server-side ControlConnRegistry entry and
	// the client message loop.
	//
	//   serverSide — registered in ctrlConns, the SOCKS proxy WRITES here
	//               (MsgSOCKSConnect), and the server-side reader READS here
	//               (MsgSOCKSReady).
	//   clientSide — the fake client READS MsgSOCKSConnect and WRITES MsgSOCKSReady.
	serverSide, clientSide := net.Pipe()

	// Register the server-side half so the SOCKS proxy can send messages.
	clientID := "fake-client-1"
	env.ctrlConns.Register(clientID, serverSide)

	// Server-side reader: processes replies that come from the client side.
	// This replaces the role of HandleControlConn's message loop for the
	// MsgSOCKSReady case.
	go func() {
		defer serverSide.Close()
		for {
			msg, err := protocol.ReadMessage(serverSide)
			if err != nil {
				return
			}
			if msg.Type == protocol.MsgSOCKSReady {
				var ready protocol.SOCKSReady
				if err := gob.NewDecoder(bytes.NewReader(msg.Payload)).Decode(&ready); err != nil {
					log.Error("server-side reader: decode SOCKSReady", "err", err)
					continue
				}
				if err := env.mgr.FulfillSOCKS(ready.ConnID, ready.Success, ready.Error); err != nil {
					log.Error("server-side reader: FulfillSOCKS", "err", err)
				}
			}
		}
	}()

	// Client-side reader: handles MsgSOCKSConnect.
	go func() {
		defer clientSide.Close()
		for {
			env2 := env // capture
			msg, err := protocol.ReadMessage(clientSide)
			if err != nil {
				return
			}
			if msg.Type != protocol.MsgSOCKSConnect {
				continue
			}
			var sc protocol.SOCKSConnect
			if err := gob.NewDecoder(bytes.NewReader(msg.Payload)).Decode(&sc); err != nil {
				log.Error("fake client: decode SOCKSConnect", "err", err)
				continue
			}

			go handleFakeSOCKSConnect(sc, clientSide, env2.dataAddr, log)
		}
	}()

	return func() {
		env.ctrlConns.Deregister(clientID)
		serverSide.Close()
		clientSide.Close()
	}
}

// handleFakeSOCKSConnect mimics tunnel.HandleSOCKSConnect for test purposes.
func handleFakeSOCKSConnect(
	sc protocol.SOCKSConnect,
	ctrlConn net.Conn,
	serverDataAddr string,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
		Error(string, ...any)
	},
) {
	targetAddr := fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort)

	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		_ = protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	dataConn, err := net.Dial("tcp", serverDataAddr)
	if err != nil {
		targetConn.Close()
		_ = protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	if err := protocol.WriteMessage(dataConn, protocol.MsgDataConnHello, protocol.DataConnHello{
		ConnID: sc.ConnID,
	}); err != nil {
		targetConn.Close()
		dataConn.Close()
		_ = protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	if err := protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
		ConnID:  sc.ConnID,
		Success: true,
	}); err != nil {
		targetConn.Close()
		dataConn.Close()
		return
	}

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(targetConn, dataConn)
		if tc, ok := targetConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(dataConn, targetConn)
		if tc, ok := dataConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	targetConn.Close()
	dataConn.Close()
}

// startEchoServer starts a local TCP echo server. Returns the listen address.
func startEchoServer(t *testing.T, ctx context.Context) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}

	go func() {
		defer ln.Close()
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// ---------------------------------------------------------------------------
// SOCKS5 dial helpers
// ---------------------------------------------------------------------------

// dialSOCKS5NoAuth performs the SOCKS5 handshake with NO_AUTH and CONNECT
// to the given target host:port. Returns the connected net.Conn ready for data.
func dialSOCKS5NoAuth(t *testing.T, socksAddr, targetHost string, targetPort int) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5 server: %v", err)
	}

	// Greeting: VER=5, NMETHODS=1, METHOD=0 (NO_AUTH)
	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		conn.Close()
		t.Fatalf("write greeting: %v", err)
	}

	// Server choice
	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		conn.Close()
		t.Fatalf("read server choice: %v", err)
	}
	if choice[0] != 0x05 || choice[1] != 0x00 {
		conn.Close()
		t.Fatalf("unexpected server choice: %v", choice)
	}

	// CONNECT request
	if err := sendConnectRequest(conn, targetHost, targetPort); err != nil {
		conn.Close()
		t.Fatalf("send CONNECT: %v", err)
	}

	// Read reply
	if err := readConnectReply(conn); err != nil {
		conn.Close()
		t.Fatalf("read CONNECT reply: %v", err)
	}

	return conn
}

// dialSOCKS5Auth performs the full SOCKS5 + RFC 1929 auth handshake.
func dialSOCKS5Auth(t *testing.T, socksAddr, user, pass, targetHost string, targetPort int) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5 server: %v", err)
	}

	// Greeting: VER=5, NMETHODS=1, METHOD=2 (USER/PASS)
	_, err = conn.Write([]byte{0x05, 0x01, 0x02})
	if err != nil {
		conn.Close()
		t.Fatalf("write greeting: %v", err)
	}

	// Server choice
	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		conn.Close()
		t.Fatalf("read server choice: %v", err)
	}
	if choice[0] != 0x05 || choice[1] != 0x02 {
		conn.Close()
		t.Fatalf("unexpected server choice: want [5 2] got %v", choice)
	}

	// Auth sub-negotiation (RFC 1929)
	uBytes := []byte(user)
	pBytes := []byte(pass)
	authMsg := make([]byte, 0, 3+len(uBytes)+len(pBytes))
	authMsg = append(authMsg, 0x01)
	authMsg = append(authMsg, byte(len(uBytes)))
	authMsg = append(authMsg, uBytes...)
	authMsg = append(authMsg, byte(len(pBytes)))
	authMsg = append(authMsg, pBytes...)
	if _, err := conn.Write(authMsg); err != nil {
		conn.Close()
		t.Fatalf("write auth: %v", err)
	}

	// Auth response
	authResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, authResp); err != nil {
		conn.Close()
		t.Fatalf("read auth response: %v", err)
	}
	if authResp[1] != 0x00 {
		conn.Close()
		t.Fatalf("auth failed: status %d", authResp[1])
	}

	// CONNECT request
	if err := sendConnectRequest(conn, targetHost, targetPort); err != nil {
		conn.Close()
		t.Fatalf("send CONNECT: %v", err)
	}

	// Read reply
	if err := readConnectReply(conn); err != nil {
		conn.Close()
		t.Fatalf("read CONNECT reply: %v", err)
	}

	return conn
}

func sendConnectRequest(conn net.Conn, host string, port int) error {
	// Try to parse as IP first; otherwise use domain type.
	ip := net.ParseIP(host)
	var req []byte

	if ip4 := ip.To4(); ip4 != nil {
		req = make([]byte, 10)
		req[0] = 0x05
		req[1] = 0x01 // CONNECT
		req[2] = 0x00 // RSV
		req[3] = 0x01 // ATYP IPv4
		copy(req[4:8], ip4)
		binary.BigEndian.PutUint16(req[8:10], uint16(port))
	} else if ip6 := ip.To16(); ip6 != nil && ip != nil {
		req = make([]byte, 22)
		req[0] = 0x05
		req[1] = 0x01
		req[2] = 0x00
		req[3] = 0x04 // ATYP IPv6
		copy(req[4:20], ip6)
		binary.BigEndian.PutUint16(req[20:22], uint16(port))
	} else {
		// Domain name
		hBytes := []byte(host)
		req = make([]byte, 7+len(hBytes))
		req[0] = 0x05
		req[1] = 0x01
		req[2] = 0x00
		req[3] = 0x03 // ATYP domain
		req[4] = byte(len(hBytes))
		copy(req[5:], hBytes)
		binary.BigEndian.PutUint16(req[5+len(hBytes):], uint16(port))
	}

	_, err := conn.Write(req)
	return err
}

func readConnectReply(conn net.Conn) error {
	// VER + REP + RSV + ATYP = 4 bytes, then addr + port
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return fmt.Errorf("read reply header: %w", err)
	}
	if hdr[1] != 0x00 {
		return fmt.Errorf("CONNECT failed, REP=0x%02x", hdr[1])
	}

	// Consume remaining address bytes.
	switch hdr[3] {
	case 0x01: // IPv4
		tail := make([]byte, 4+2)
		_, _ = io.ReadFull(conn, tail)
	case 0x03: // domain
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			return err
		}
		tail := make([]byte, int(lb[0])+2)
		_, _ = io.ReadFull(conn, tail)
	case 0x04: // IPv6
		tail := make([]byte, 16+2)
		_, _ = io.ReadFull(conn, tail)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSOCKS5_NoAuth verifies the no-authentication path:
// SOCKS5 client connects, server selects NO_AUTH, client sends CONNECT to
// a local echo server, data flows bidirectionally.
func TestSOCKS5_NoAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env, cleanup := startTestEnv(t, "", "")
	defer cleanup()

	connectFakeClient(t, env)

	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	// Allow listeners to settle.
	time.Sleep(30 * time.Millisecond)

	conn := dialSOCKS5NoAuth(t, env.socksAddr, echoHost, echoPort)
	defer conn.Close()

	// Send data and verify echo.
	msg := []byte("hello socks5 no-auth")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q, want %q", buf, msg)
	}
}

// TestSOCKS5_Auth verifies the username/password authentication path (RFC 1929).
func TestSOCKS5_Auth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const user, pass = "alice", "s3cr3t"
	env, cleanup := startTestEnv(t, user, pass)
	defer cleanup()

	connectFakeClient(t, env)

	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	time.Sleep(30 * time.Millisecond)

	conn := dialSOCKS5Auth(t, env.socksAddr, user, pass, echoHost, echoPort)
	defer conn.Close()

	msg := []byte("hello socks5 with-auth")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q, want %q", buf, msg)
	}
}

// TestSOCKS5_WrongAuth verifies that wrong credentials are rejected.
func TestSOCKS5_WrongAuth(t *testing.T) {
	const user, pass = "alice", "s3cr3t"
	env, cleanup := startTestEnv(t, user, pass)
	defer cleanup()

	connectFakeClient(t, env)

	conn, err := net.DialTimeout("tcp", env.socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Greeting: request USER/PASS method.
	_, _ = conn.Write([]byte{0x05, 0x01, 0x02})

	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		t.Fatalf("read choice: %v", err)
	}
	if choice[1] != 0x02 {
		t.Fatalf("expected method 2, got %d", choice[1])
	}

	// Send wrong password.
	wrongPass := "wrongpass"
	uBytes := []byte(user)
	pBytes := []byte(wrongPass)
	authMsg := []byte{0x01, byte(len(uBytes))}
	authMsg = append(authMsg, uBytes...)
	authMsg = append(authMsg, byte(len(pBytes)))
	authMsg = append(authMsg, pBytes...)
	_, _ = conn.Write(authMsg)

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read auth resp: %v", err)
	}
	if resp[1] == 0x00 {
		t.Error("expected auth failure, but server accepted wrong password")
	}
}

// TestSOCKS5_NoClientConnected verifies that the server returns a failure reply
// when no client tunnel is connected.
func TestSOCKS5_NoClientConnected(t *testing.T) {
	env, cleanup := startTestEnv(t, "", "")
	defer cleanup()
	// Deliberately do NOT connect a fake client.

	conn, err := net.DialTimeout("tcp", env.socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Handshake: NO_AUTH
	_, _ = conn.Write([]byte{0x05, 0x01, 0x00})
	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		t.Fatalf("read choice: %v", err)
	}

	// CONNECT to any address.
	if err := sendConnectRequest(conn, "127.0.0.1", 9999); err != nil {
		t.Fatalf("send CONNECT: %v", err)
	}

	// Read reply — should be a failure code.
	replyHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, replyHdr); err != nil {
		t.Fatalf("read reply hdr: %v", err)
	}
	if replyHdr[1] == 0x00 {
		t.Error("expected failure reply when no client is connected, got success")
	}
}

// TestSOCKS5_WithRealClientHandler tests the end-to-end flow using
// tunnel.HandleSOCKSConnect (the real client-side implementation) driven by
// an in-process net.Pipe control connection, with a server-side reader
// processing MsgSOCKSReady (mirroring HandleControlConn).
func TestSOCKS5_WithRealClientHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tunnel.NewManager()
	ctrlConns := tunnel.NewControlConnRegistry()
	log := logger.New("test-real")

	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", mgr, log); err != nil {
		t.Fatalf("StartDataListener: %v", err)
	}
	dataAddr := tunnel.DataAddr

	if err := socks.StartSOCKSProxy(ctx, "127.0.0.1:0", mgr, ctrlConns, dataAddr, log, "", ""); err != nil {
		t.Fatalf("StartSOCKSProxy: %v", err)
	}
	socksProxyAddr := socks.LastSOCKSAddr

	// Use net.Pipe() to simulate the control channel.
	serverSide, clientSide := net.Pipe()
	clientID := "real-client-1"
	ctrlConns.Register(clientID, serverSide)
	defer func() {
		ctrlConns.Deregister(clientID)
		serverSide.Close()
		clientSide.Close()
	}()

	// Server-side reader: processes MsgSOCKSReady (mirrors HandleControlConn).
	go func() {
		for {
			msg, err := protocol.ReadMessage(serverSide)
			if err != nil {
				return
			}
			if msg.Type == protocol.MsgSOCKSReady {
				var ready protocol.SOCKSReady
				if err := gob.NewDecoder(bytes.NewReader(msg.Payload)).Decode(&ready); err != nil {
					continue
				}
				_ = mgr.FulfillSOCKS(ready.ConnID, ready.Success, ready.Error)
			}
		}
	}()

	// Client-side message loop: uses the real tunnel.HandleSOCKSConnect.
	go func() {
		for {
			msg, err := protocol.ReadMessage(clientSide)
			if err != nil {
				return
			}
			if msg.Type == protocol.MsgSOCKSConnect {
				var sc protocol.SOCKSConnect
				if err := gob.NewDecoder(bytes.NewReader(msg.Payload)).Decode(&sc); err != nil {
					continue
				}
				tunnel.HandleSOCKSConnect(sc, clientSide, dataAddr, log)
			}
		}
	}()

	// Start a local echo target.
	echoAddr := startEchoServer(t, ctx)
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	echoPort := 0
	fmt.Sscan(echoPortStr, &echoPort)

	time.Sleep(30 * time.Millisecond)

	// Connect via SOCKS5.
	socksConn := dialSOCKS5NoAuth(t, socksProxyAddr, echoHost, echoPort)
	defer socksConn.Close()

	msg := []byte("end-to-end socks5 real handler test")
	if _, err := socksConn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = socksConn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(socksConn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q, want %q", buf, msg)
	}
}
