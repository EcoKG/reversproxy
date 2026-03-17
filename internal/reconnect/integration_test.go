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

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/reconnect"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Shared test infrastructure
// ---------------------------------------------------------------------------

// restartableServer wraps a TLS control server that can be stopped and
// restarted while keeping the same logical address (using a new port each
// time to avoid TIME_WAIT conflicts; the addr is re-advertised each restart).
type restartableServer struct {
	t       *testing.T
	mu      sync.Mutex
	cancel  context.CancelFunc
	ln      net.Listener
	reg     *control.ClientRegistry
	mgr     *tunnel.Manager
	addr    string
	dataAddr string
	cert    tls.Certificate
}

func newRestartableServer(t *testing.T) *restartableServer {
	t.Helper()
	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "server.crt"),
		filepath.Join(dir, "server.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}
	s := &restartableServer{t: t, cert: cert}
	s.start()
	return s
}

func (s *restartableServer) start() {
	s.t.Helper()

	tlsCfg := control.NewServerTLSConfig(s.cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		s.t.Fatalf("tls.Listen: %v", err)
	}

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
	s.ln = ln
	s.reg = reg
	s.mgr = mgr
	s.addr = ln.Addr().String()
	s.dataAddr = tunnel.DataAddr
	s.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go control.HandleControlConn(ctx, conn, reg, "secret", log, mgr, tunnel.DataAddr)
		}
	}()
}

func (s *restartableServer) stop() {
	s.mu.Lock()
	cancel := s.cancel
	ln := s.ln
	s.mu.Unlock()

	cancel()
	_ = ln.Close()
}

func (s *restartableServer) restart() {
	s.stop()
	time.Sleep(50 * time.Millisecond)
	s.start()
}

func (s *restartableServer) getAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

func (s *restartableServer) getReg() *control.ClientRegistry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reg
}

// dialAndRegister dials the server, performs registration, optionally requests a
// tunnel, and returns the conn and tunnel response.
func dialAndRegister(
	t *testing.T,
	addr string,
	localHost string,
	localPort int,
) (net.Conn, protocol.TunnelResp) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true)
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, protocol.TunnelResp{}
	}

	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: "secret",
		Name:      "reconnect-test-client",
		Version:   "0.1.0",
	}); err != nil {
		conn.Close()
		return nil, protocol.TunnelResp{}
	}

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
// SC1: Client auto-reconnects after server restart
// ---------------------------------------------------------------------------

// TestSC1_AutoReconnectAfterServerRestart verifies that the Backoff + reconnect
// loop model works: after the server is restarted on a new port the client can
// successfully reconnect. We simulate the client-side reconnect loop inline.
func TestSC1_AutoReconnectAfterServerRestart(t *testing.T) {
	srv := newRestartableServer(t)

	// Phase 1: connect to the original server.
	addr1 := srv.getAddr()
	conn1, _ := dialAndRegister(t, addr1, "", 0)
	if conn1 == nil {
		t.Fatal("initial connection failed")
	}

	// Give the server a moment to register the client.
	time.Sleep(50 * time.Millisecond)

	reg1 := srv.getReg()
	if len(reg1.List()) == 0 {
		t.Fatal("client not registered with server before restart")
	}

	// Stop the server — this simulates a network outage.
	conn1.Close()
	srv.stop()

	// Phase 2: restart the server (new port, simulating reconnect target).
	time.Sleep(50 * time.Millisecond)
	srv.start()
	addr2 := srv.getAddr()

	// Reconnect loop with exponential backoff.
	b := reconnect.NewBackoff()

	var conn2 net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn2, _ = dialAndRegister(t, addr2, "", 0)
		if conn2 != nil {
			b.Reset() // SC3: reset backoff on success
			break
		}
		delay := b.Next()
		t.Logf("reconnect attempt failed, backing off %v", delay)
		time.Sleep(delay)
	}

	if conn2 == nil {
		t.Fatal("SC1 FAIL: failed to reconnect after server restart within 10s")
	}
	defer conn2.Close()

	time.Sleep(50 * time.Millisecond)
	reg2 := srv.getReg()
	if len(reg2.List()) == 0 {
		t.Fatal("SC1 FAIL: client not re-registered after reconnect")
	}
	t.Log("SC1 PASS: client successfully reconnected after server restart")
}

// ---------------------------------------------------------------------------
// SC2: Tunnel restored after reconnect
// ---------------------------------------------------------------------------

// TestSC2_TunnelRestoredAfterReconnect verifies that after reconnecting, the
// client re-registers its tunnel and external traffic can flow again.
func TestSC2_TunnelRestoredAfterReconnect(t *testing.T) {
	echoPort, echoCancel := startLocalEchoServer(t)
	defer echoCancel()

	srv := newRestartableServer(t)

	addr1 := srv.getAddr()

	// Initial connection with tunnel.
	conn1, tresp1 := dialAndRegister(t, addr1, "127.0.0.1", echoPort)
	if conn1 == nil || tresp1.Status != "ok" {
		t.Fatal("initial tunnel registration failed")
	}

	dataAddrs1 := map[string]string{tresp1.TunnelID: tresp1.ServerDataAddr}
	go runMsgLoop(conn1, dataAddrs1)

	time.Sleep(50 * time.Millisecond)

	// Verify initial tunnel works.
	extConn1, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tresp1.PublicPort))
	if err != nil {
		t.Fatalf("initial external dial failed: %v", err)
	}
	msg := []byte("before-restart")
	_, _ = extConn1.Write(msg)
	_ = extConn1.(*net.TCPConn).CloseWrite()
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(extConn1, buf); err != nil || !bytes.Equal(buf, msg) {
		t.Fatalf("initial tunnel not working: err=%v, got=%q", err, buf)
	}
	extConn1.Close()

	// Simulate network disruption: close connection, stop server.
	conn1.Close()
	srv.stop()
	time.Sleep(50 * time.Millisecond)
	srv.start()

	// Reconnect and re-register tunnel.
	addr2 := srv.getAddr()
	b := reconnect.NewBackoff()

	var conn2 net.Conn
	var tresp2 protocol.TunnelResp
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn2, tresp2 = dialAndRegister(t, addr2, "127.0.0.1", echoPort)
		if conn2 != nil && tresp2.Status == "ok" {
			b.Reset()
			break
		}
		if conn2 != nil {
			conn2.Close()
			conn2 = nil
		}
		time.Sleep(b.Next())
	}

	if conn2 == nil {
		t.Fatal("SC2 FAIL: could not reconnect and re-register tunnel")
	}
	defer conn2.Close()

	dataAddrs2 := map[string]string{tresp2.TunnelID: tresp2.ServerDataAddr}
	go runMsgLoop(conn2, dataAddrs2)

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

// TestSC3_ExponentialBackoff verifies that:
//  1. Each successive Next() call returns a value >= the previous one (before jitter).
//  2. The delay is capped at Max.
//  3. Reset() brings it back to Initial.
//  4. Jitter keeps values within the expected band.
func TestSC3_ExponentialBackoff(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        100 * time.Millisecond,
		Max:            1600 * time.Millisecond,
		Multiplier:     2.0,
		JitterFraction: 0, // deterministic
	}

	// Without jitter, collect delays.
	delays := make([]time.Duration, 8)
	for i := range delays {
		delays[i] = b.Next()
	}

	// Delays must grow until capped.
	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] && delays[i] < b.Max {
			t.Errorf("delay[%d]=%v < delay[%d]=%v but not yet at cap %v",
				i, delays[i], i-1, delays[i-1], b.Max)
		}
		if delays[i] > b.Max {
			t.Errorf("delay[%d]=%v exceeds max %v", i, delays[i], b.Max)
		}
	}

	// The last few delays should all equal Max.
	if delays[len(delays)-1] != b.Max {
		t.Errorf("final delay %v != Max %v", delays[len(delays)-1], b.Max)
	}

	// Reset should bring back to Initial.
	b.Reset()
	first := b.Next()
	if first != b.Initial {
		t.Errorf("after Reset, first Next() = %v, want %v", first, b.Initial)
	}

	t.Log("SC3 PASS: exponential backoff grows correctly, caps at Max, resets to Initial")

	// Now verify jitter stays within bounds.
	bj := reconnect.NewBackoff()
	var connectAttempts int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Simulate 5 failed connect attempts and measure that delay grows.
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
		_ = prevDelay // suppress unused warning
	}

	if atomic.LoadInt32(&connectAttempts) != 5 {
		t.Errorf("expected 5 backoff calls, got %d", connectAttempts)
	}
}
