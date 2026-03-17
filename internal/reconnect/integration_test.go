package reconnect_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Shared test infrastructure — new connection direction
//
// In the new architecture the CLIENT listens and the SERVER dials.
// We model that here:
//   - restartableServer dials to a client listener address. It reconnects when
//     the underlying connection is lost.
//   - The "client" side is modelled by listenAndAccept helpers that start a
//     TLS listener, accept one connection, perform the new handshake (read
//     ClientRegister, validate, send RegisterResp), and then return the conn.
// ---------------------------------------------------------------------------

// clientListener wraps a TLS listener that plays the client role.
type clientListener struct {
	t    *testing.T
	mu   sync.Mutex
	ln   net.Listener
	addr string
	cert tls.Certificate
}

func newClientListener(t *testing.T) *clientListener {
	t.Helper()
	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "client.crt"),
		filepath.Join(dir, "client.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}
	cl := &clientListener{t: t, cert: cert}
	cl.rebind()
	return cl
}

// rebind starts a fresh listener on a new port.
func (cl *clientListener) rebind() {
	cl.t.Helper()
	tlsCfg := control.NewServerTLSConfig(cl.cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		cl.t.Fatalf("client tls.Listen: %v", err)
	}
	cl.mu.Lock()
	cl.ln = ln
	cl.addr = ln.Addr().String()
	cl.mu.Unlock()
}

func (cl *clientListener) getAddr() string {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.addr
}

func (cl *clientListener) close() {
	cl.mu.Lock()
	ln := cl.ln
	cl.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// acceptOne accepts a single connection on the client listener, performs the
// new handshake (reads ClientRegister, validates token, sends RegisterResp),
// and returns the conn along with optional tunnel info from a RequestTunnel.
// Returns nil on failure (e.g. listener was closed).
func (cl *clientListener) acceptOne(token, clientName string) net.Conn {
	cl.mu.Lock()
	ln := cl.ln
	cl.mu.Unlock()

	conn, err := ln.Accept()
	if err != nil {
		return nil
	}

	// Client-side handshake.
	env, err := protocol.ReadMessage(conn)
	if err != nil || env.Type != protocol.MsgClientRegister {
		conn.Close()
		return nil
	}
	var reg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&reg); err != nil {
		conn.Close()
		return nil
	}
	if reg.AuthToken != token {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "invalid token",
		})
		conn.Close()
		return nil
	}
	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: clientName,
	}); err != nil {
		conn.Close()
		return nil
	}
	return conn
}

// restartableServer simulates the server side: it dials to a client listener
// and calls HandleControlConn.  On restart it dials a new address.
type restartableServer struct {
	t        *testing.T
	mu       sync.Mutex
	cancel   context.CancelFunc
	reg      *control.ClientRegistry
	mgr      *tunnel.Manager
	dataAddr string
}

func newRestartableServer(t *testing.T) *restartableServer {
	t.Helper()
	s := &restartableServer{t: t}
	return s
}

// dialClient makes the server dial clientAddr and call HandleControlConn.
// Returns when the connection is established and handed off to a goroutine.
func (s *restartableServer) dialClient(clientAddr string) {
	s.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	reg := control.NewClientRegistry()
	mgr := tunnel.NewManager()
	log := logger.New("test-server")

	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", mgr, log); err != nil {
		cancel()
		s.t.Fatalf("StartDataListener: %v", err)
	}

	s.mu.Lock()
	s.cancel = cancel
	s.reg = reg
	s.mgr = mgr
	s.dataAddr = tunnel.DataAddr
	s.mu.Unlock()

	tlsCfg := control.NewClientTLSConfig(true)
	go func() {
		conn, err := tls.Dial("tcp", clientAddr, tlsCfg)
		if err != nil {
			cancel()
			return
		}
		control.HandleControlConn(ctx, conn, reg, "secret", log, mgr, tunnel.DataAddr)
	}()
}

func (s *restartableServer) stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *restartableServer) getReg() *control.ClientRegistry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reg
}

func (s *restartableServer) getDataAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataAddr
}

// dialAndRegister is used on the SERVER side: it dials clientAddr, performs the
// new handshake (sends ClientRegister, reads RegisterResp), optionally requests
// a tunnel, and returns the conn and tunnel response.
func dialAndRegister(
	t *testing.T,
	clientAddr string,
	localHost string,
	localPort int,
) (net.Conn, protocol.TunnelResp) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true)
	conn, err := tls.Dial("tcp", clientAddr, tlsCfg)
	if err != nil {
		return nil, protocol.TunnelResp{}
	}

	// Server sends ClientRegister.
	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: "secret",
		Name:      "server",
		Version:   "0.1.0",
	}); err != nil {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	// Server reads RegisterResp from client.
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	var resp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&resp); err != nil || resp.Status != "ok" {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	if localPort == 0 {
		return conn, protocol.TunnelResp{}
	}

	if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, protocol.RequestTunnel{
		LocalHost: localHost,
		LocalPort: localPort,
	}); err != nil {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	tenv, err := protocol.ReadMessage(conn)
	if err != nil || tenv.Type != protocol.MsgTunnelResp {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	var tresp protocol.TunnelResp
	if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil || tresp.Status != "ok" {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

	return conn, tresp
}

// runMsgLoop drains the control conn and handles OpenConnection messages.
func runMsgLoop(conn net.Conn, dataAddrs map[string]string) {
	log := logger.New("test-client-loop")
	for {
		env, err := protocol.ReadMessage(conn)
		if err != nil {
			return
		}
		switch env.Type {
		case protocol.MsgOpenConnection:
			var oc protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&oc); err != nil {
				continue
			}
			da, ok := dataAddrs[oc.TunnelID]
			if !ok {
				continue
			}
			tunnel.HandleOpenConnection(oc, da, log)
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				continue
			}
			_ = protocol.WriteMessage(conn, protocol.MsgPong, protocol.Pong{Seq: ping.Seq})
		}
	}
}

// startLocalEchoServer starts a local TCP echo server on a random port.
func startLocalEchoServer(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer ln.Close()
		go func() { <-ctx.Done(); _ = ln.Close() }()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return port, cancel
}

// ---------------------------------------------------------------------------
// SC1: Server auto-reconnects after client listener restart
// ---------------------------------------------------------------------------

// TestSC1_AutoReconnectAfterServerRestart verifies that after the client
// listener restarts on a new port, the server (with backoff) successfully
// reconnects to the new address.
func TestSC1_AutoReconnectAfterServerRestart(t *testing.T) {
	// Phase 1: start client listener.
	cl := newClientListener(t)
	defer cl.close()

	addr1 := cl.getAddr()

	// Accept the client-side handshake in a goroutine.
	connCh1 := make(chan net.Conn, 1)
	go func() {
		conn := cl.acceptOne("secret", "reconnect-test-client")
		connCh1 <- conn
	}()

	// Server dials the client.
	srv := newRestartableServer(t)
	srv.dialClient(addr1)

	// Wait for the server to connect and complete registration.
	var clientConn1 net.Conn
	select {
	case clientConn1 = <-connCh1:
	case <-time.After(3 * time.Second):
		t.Fatal("SC1: timed out waiting for initial server connection")
	}
	if clientConn1 == nil {
		t.Fatal("SC1: initial connection failed")
	}

	time.Sleep(50 * time.Millisecond)
	if len(srv.getReg().List()) == 0 {
		t.Fatal("SC1: client not registered with server before restart")
	}

	// Close the client connection and restart the client listener on a new port.
	clientConn1.Close()
	cl.close()
	time.Sleep(50 * time.Millisecond)
	cl.rebind()
	addr2 := cl.getAddr()

	// Accept the reconnected server on the new listener.
	connCh2 := make(chan net.Conn, 1)
	go func() {
		conn := cl.acceptOne("secret", "reconnect-test-client")
		connCh2 <- conn
	}()

	// Server-side reconnect loop with exponential backoff.
	b := reconnect.NewBackoff()

	var serverConn2 net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		serverConn2, _ = dialAndRegister(t, addr2, "", 0)
		if serverConn2 != nil {
			b.Reset()
			break
		}
		delay := b.Next()
		t.Logf("SC1: reconnect attempt failed, backing off %v", delay)
		time.Sleep(delay)
	}

	if serverConn2 == nil {
		t.Fatal("SC1 FAIL: failed to reconnect after client listener restart within 10s")
	}
	defer serverConn2.Close()

	// Wait for the client to accept and acknowledge.
	select {
	case conn := <-connCh2:
		if conn == nil {
			t.Fatal("SC1 FAIL: client-side handshake failed after reconnect")
		}
		defer conn.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("SC1 FAIL: timed out waiting for client to accept reconnection")
	}

	t.Log("SC1 PASS: server successfully reconnected after client listener restart")
}

// ---------------------------------------------------------------------------
// SC2: Tunnel restored after reconnect
// ---------------------------------------------------------------------------

// TestSC2_TunnelRestoredAfterReconnect verifies that after the client listener
// restarts on a new address, the server can re-establish the connection and
// re-register tunnels with working external traffic.
//
// In the new architecture the SERVER dials the CLIENT. We use restartableServer
// (which calls HandleControlConn) on the server side and a clientListener that
// performs the reversed handshake and message loop on the client side.
func TestSC2_TunnelRestoredAfterReconnect(t *testing.T) {
	echoPort, echoCancel := startLocalEchoServer(t)
	defer echoCancel()

	// --- Phase 1: initial connection using full HandleControlConn flow ---

	cl1 := newClientListener(t)
	defer cl1.close()
	addr1 := cl1.getAddr()

	// Client-side: accept connection, handle handshake + message loop + tunnel requests.
	srv1 := newRestartableServer(t)
	srv1.dialClient(addr1)

	// The client-side acceptOne accepts one connection and returns it.
	// We then sequentially: send RequestTunnel, read TunnelResp, then start ping loop.
	clientConn1Ch := make(chan net.Conn, 1)
	go func() {
		conn := cl1.acceptOne("secret", "reconnect-test-client")
		clientConn1Ch <- conn
	}()

	// Wait for server to connect (client-side handshake done).
	var clientConn1 net.Conn
	select {
	case clientConn1 = <-clientConn1Ch:
		if clientConn1 == nil {
			t.Fatal("SC2: client-side handshake failed on initial connect")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SC2: timed out waiting for initial server→client connection")
	}

	// Wait for HandleControlConn to register the client in srv1.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv1.getReg().List()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(srv1.getReg().List()) == 0 {
		t.Fatal("SC2: client not registered with server after initial connection")
	}

	// Request a tunnel through the established connection (client-side).
	// Send RequestTunnel through clientConn1, which HandleControlConn will process.
	// We must drain Pings before/between the TunnelResp to avoid read races.
	var tresp1 protocol.TunnelResp
	{
		if err := protocol.WriteMessage(clientConn1, protocol.MsgRequestTunnel, protocol.RequestTunnel{
			LocalHost: "127.0.0.1",
			LocalPort: echoPort,
		}); err != nil {
			t.Fatalf("SC2: send RequestTunnel failed: %v", err)
		}
		// Read until we get TunnelResp, handling any interleaved Pings.
		for {
			env, err := protocol.ReadMessage(clientConn1)
			if err != nil {
				t.Fatalf("SC2: read message failed waiting for TunnelResp: %v", err)
			}
			if env.Type == protocol.MsgPing {
				var ping protocol.Ping
				_ = gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping)
				_ = protocol.WriteMessage(clientConn1, protocol.MsgPong, protocol.Pong{Seq: ping.Seq})
				continue
			}
			if env.Type != protocol.MsgTunnelResp {
				t.Fatalf("SC2: expected TunnelResp, got type=%v", env.Type)
			}
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&tresp1); err != nil || tresp1.Status != "ok" {
				t.Fatalf("SC2: TunnelResp failed: err=%v status=%v", err, tresp1.Status)
			}
			break
		}
	}

	// Run message loop so OpenConnection messages and future Pings are handled.
	go func() {
		da := map[string]string{tresp1.TunnelID: tresp1.ServerDataAddr}
		runMsgLoop(clientConn1, da)
	}()

	time.Sleep(50 * time.Millisecond)

	// Verify initial tunnel works.
	extConn1, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tresp1.PublicPort))
	if err != nil {
		t.Fatalf("SC2: initial external dial failed: %v", err)
	}
	msg := []byte("before-restart")
	_, _ = extConn1.Write(msg)
	_ = extConn1.(*net.TCPConn).CloseWrite()
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(extConn1, buf); err != nil || !bytes.Equal(buf, msg) {
		t.Fatalf("SC2: initial tunnel not working: err=%v, got=%q", err, buf)
	}
	extConn1.Close()

	// --- Phase 2: simulate network disruption, restart client listener ---

	clientConn1.Close()
	cl1.close()
	time.Sleep(50 * time.Millisecond)

	// Restart client on new port.
	cl2 := newClientListener(t)
	defer cl2.close()
	addr2 := cl2.getAddr()

	// New restartable server dials the new client listener.
	srv2 := newRestartableServer(t)
	srv2.dialClient(addr2)

	clientConn2Ch := make(chan net.Conn, 1)
	go func() {
		conn := cl2.acceptOne("secret", "reconnect-test-client")
		clientConn2Ch <- conn
	}()

	var clientConn2 net.Conn
	select {
	case clientConn2 = <-clientConn2Ch:
		if clientConn2 == nil {
			t.Fatal("SC2 FAIL: client-side handshake failed after restart")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SC2 FAIL: timed out waiting for server→client reconnection")
	}
	defer clientConn2.Close()

	// Wait for registration.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv2.getReg().List()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(srv2.getReg().List()) == 0 {
		t.Fatal("SC2 FAIL: client not re-registered after reconnect")
	}

	// Re-register tunnel. Read until TunnelResp, draining interleaved Pings.
	var tresp2 protocol.TunnelResp
	{
		if err := protocol.WriteMessage(clientConn2, protocol.MsgRequestTunnel, protocol.RequestTunnel{
			LocalHost: "127.0.0.1",
			LocalPort: echoPort,
		}); err != nil {
			t.Fatalf("SC2 FAIL: send RequestTunnel after reconnect: %v", err)
		}
		for {
			env, err := protocol.ReadMessage(clientConn2)
			if err != nil {
				t.Fatalf("SC2 FAIL: read message after reconnect: %v", err)
			}
			if env.Type == protocol.MsgPing {
				var ping protocol.Ping
				_ = gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping)
				_ = protocol.WriteMessage(clientConn2, protocol.MsgPong, protocol.Pong{Seq: ping.Seq})
				continue
			}
			if env.Type != protocol.MsgTunnelResp {
				t.Fatalf("SC2 FAIL: expected TunnelResp after reconnect, got type=%v", env.Type)
			}
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&tresp2); err != nil || tresp2.Status != "ok" {
				t.Fatalf("SC2 FAIL: TunnelResp after reconnect: err=%v status=%v", err, tresp2.Status)
			}
			break
		}
	}

	go func() {
		da := map[string]string{tresp2.TunnelID: tresp2.ServerDataAddr}
		runMsgLoop(clientConn2, da)
	}()

	time.Sleep(50 * time.Millisecond)

	// Verify tunnel works after reconnect.
	extConn2, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tresp2.PublicPort))
	if err != nil {
		t.Fatalf("SC2 FAIL: external dial after reconnect failed: %v", err)
	}
	defer extConn2.Close()

	msg2 := []byte("after-restart")
	_, _ = extConn2.Write(msg2)
	_ = extConn2.(*net.TCPConn).CloseWrite()
	buf2 := make([]byte, len(msg2))
	if _, err := io.ReadFull(extConn2, buf2); err != nil {
		t.Fatalf("SC2 FAIL: read from tunnel after reconnect failed: %v", err)
	}
	if !bytes.Equal(buf2, msg2) {
		t.Fatalf("SC2 FAIL: data mismatch after reconnect: got %q, want %q", buf2, msg2)
	}
	t.Log("SC2 PASS: tunnel restored after reconnect, external traffic flows again")
}

// ---------------------------------------------------------------------------
// SC3: Exponential backoff behavior
// ---------------------------------------------------------------------------

// TestSC3_ExponentialBackoff verifies the backoff algorithm in isolation.
// This test does not involve network connections — it only tests the Backoff type.
func TestSC3_ExponentialBackoff(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        100 * time.Millisecond,
		Max:            1600 * time.Millisecond,
		Multiplier:     2.0,
		JitterFraction: 0, // deterministic
	}

	delays := make([]time.Duration, 8)
	for i := range delays {
		delays[i] = b.Next()
	}

	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] && delays[i] < b.Max {
			t.Errorf("delay[%d]=%v < delay[%d]=%v but not yet at cap %v",
				i, delays[i], i-1, delays[i-1], b.Max)
		}
		if delays[i] > b.Max {
			t.Errorf("delay[%d]=%v exceeds max %v", i, delays[i], b.Max)
		}
	}

	if delays[len(delays)-1] != b.Max {
		t.Errorf("final delay %v != Max %v", delays[len(delays)-1], b.Max)
	}

	b.Reset()
	first := b.Next()
	if first != b.Initial {
		t.Errorf("after Reset, first Next() = %v, want %v", first, b.Initial)
	}

	t.Log("SC3 PASS: exponential backoff grows correctly, caps at Max, resets to Initial")

	bj := reconnect.NewBackoff()
	var connectAttempts int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var prevDelay time.Duration
	for i := 0; i < 5; i++ {
		select {
		case <-ctx.Done():
			t.Fatalf("SC3 FAIL: context expired during backoff test")
		default:
		}
		d := bj.Next()
		atomic.AddInt32(&connectAttempts, 1)
		if d <= 0 {
			t.Errorf("attempt %d: non-positive delay %v", i, d)
		}
		if i > 0 && d > bj.Max*(1+1) {
			t.Errorf("attempt %d: delay %v way above max %v", i, d, bj.Max)
		}
		prevDelay = d
		_ = prevDelay
	}

	if atomic.LoadInt32(&connectAttempts) != 5 {
		t.Errorf("expected 5 backoff calls, got %d", connectAttempts)
	}
}
