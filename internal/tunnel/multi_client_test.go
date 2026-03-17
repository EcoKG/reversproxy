package tunnel_test

// Phase 5: Multi-client concurrent operation integration tests.
//
// Success Criteria:
//   SC1: Multiple clients simultaneously register different TCP ports and HTTP
//        hostnames; each tunnel works independently.
//   SC2: One client's connection failure does not affect other clients' tunnels.
//   SC3: High-concurrency load through multiple tunnels stays within latency bounds.

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
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// SC1: Multiple clients — independent tunnels
// ---------------------------------------------------------------------------

// TestSC1_MultiClientIndependentTunnels connects 4 clients (3 TCP + 1 HTTP),
// each registering a different tunnel.  Verifies all tunnels work independently.
func TestSC1_MultiClientIndependentTunnels(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start 3 distinct local echo services.
	echo1 := startLocalEcho(t, ctx)
	echo2 := startLocalEcho(t, ctx)
	echo3 := startLocalEcho(t, ctx)

	// Start 1 local HTTP service.
	httpPort := startLocalHTTP(t, ctx, "sc1-http-response")

	// Connect 3 TCP tunnel clients (server dials each client listener).
	conn1, _, tresp1 := mc_connectTCP(t, infra.dialFn, "mc-tcp-1", "127.0.0.1", echo1)
	defer conn1.Close()
	go runClientMessageLoop(conn1, map[string]string{tresp1.TunnelID: tresp1.ServerDataAddr}, nil)

	conn2, _, tresp2 := mc_connectTCP(t, infra.dialFn, "mc-tcp-2", "127.0.0.1", echo2)
	defer conn2.Close()
	go runClientMessageLoop(conn2, map[string]string{tresp2.TunnelID: tresp2.ServerDataAddr}, nil)

	conn3, _, tresp3 := mc_connectTCP(t, infra.dialFn, "mc-tcp-3", "127.0.0.1", echo3)
	defer conn3.Close()
	go runClientMessageLoop(conn3, map[string]string{tresp3.TunnelID: tresp3.ServerDataAddr}, nil)

	// Connect 1 HTTP tunnel client.
	connHTTP, hresp := connectHTTPClient(t, infra, "sc1-host.local", "127.0.0.1", httpPort)
	defer connHTTP.Close()
	go runHTTPClientLoop(connHTTP, map[string]string{hresp.TunnelID: hresp.ServerDataAddr})

	// Allow all listeners to settle.
	time.Sleep(80 * time.Millisecond)

	// Verify each TCP tunnel routes to its own echo service with unique messages.
	for i, tresp := range []protocol.TunnelResp{tresp1, tresp2, tresp3} {
		idx := i + 1
		msg := []byte(fmt.Sprintf("sc1-client-%d-unique-payload", idx))
		if err := mc_doEchoRoundTrip(tresp.PublicPort, msg); err != nil {
			t.Errorf("SC1: TCP client %d: %v", idx, err)
		}
	}

	// Verify HTTP tunnel routes correctly.
	body := doHTTPRequest(t, infra.httpAddr, "sc1-host.local", "/")
	if !mc_contains(body, "sc1-http-response") {
		t.Errorf("SC1: HTTP tunnel: want 'sc1-http-response' in body, got %q", body)
	}

	// Verify all 4 clients are registered.
	clients := infra.reg.List()
	if len(clients) != 4 {
		t.Errorf("SC1: expected 4 registered clients, got %d", len(clients))
	}

	// Verify all 3 TCP public ports are distinct.
	ports := map[int]bool{
		tresp1.PublicPort: true,
		tresp2.PublicPort: true,
		tresp3.PublicPort: true,
	}
	if len(ports) != 3 {
		t.Errorf("SC1: TCP ports not unique: %v, %v, %v",
			tresp1.PublicPort, tresp2.PublicPort, tresp3.PublicPort)
	}
}

// TestSC1_MultiClientHTTPIsolation verifies that 3 HTTP clients with distinct
// hostnames do not interfere with each other.
func TestSC1_MultiClientHTTPIsolation(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := []string{"alpha.mc.local", "beta.mc.local", "gamma.mc.local"}
	bodies := []string{"resp-alpha", "resp-beta", "resp-gamma"}

	ctrlConns := make([]net.Conn, len(hosts))
	for i, h := range hosts {
		port := startLocalHTTP(t, ctx, bodies[i])
		conn, hresp := connectHTTPClient(t, infra, h, "127.0.0.1", port)
		ctrlConns[i] = conn
		go runHTTPClientLoop(conn, map[string]string{hresp.TunnelID: hresp.ServerDataAddr})
	}
	defer func() {
		for _, c := range ctrlConns {
			c.Close()
		}
	}()

	time.Sleep(80 * time.Millisecond)

	for i, h := range hosts {
		body := doHTTPRequest(t, infra.httpAddr, h, "/")
		if !mc_contains(body, bodies[i]) {
			t.Errorf("HTTP isolation: host %q: want %q in body, got %q",
				h, bodies[i], body)
		}
	}
}

// ---------------------------------------------------------------------------
// SC2: Client failure isolation
// ---------------------------------------------------------------------------

// TestSC2_ClientFailureIsolation connects 3 TCP clients, kills the middle
// one's control connection, then verifies the remaining tunnels still work
// and the manager has cleaned up the dead client's state.
func TestSC2_ClientFailureIsolation(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoA := startLocalEcho(t, ctx)
	echoB := startLocalEcho(t, ctx)
	echoC := startLocalEcho(t, ctx)

	connA, _, trespA := mc_connectTCP(t, infra.dialFn, "iso-A", "127.0.0.1", echoA)
	go runClientMessageLoop(connA, map[string]string{trespA.TunnelID: trespA.ServerDataAddr}, nil)

	// connB is the client we will kill.
	connB, _, trespB := mc_connectTCP(t, infra.dialFn, "iso-B", "127.0.0.1", echoB)
	go runClientMessageLoop(connB, map[string]string{trespB.TunnelID: trespB.ServerDataAddr}, nil)

	connC, _, trespC := mc_connectTCP(t, infra.dialFn, "iso-C", "127.0.0.1", echoC)
	go runClientMessageLoop(connC, map[string]string{trespC.TunnelID: trespC.ServerDataAddr}, nil)

	time.Sleep(80 * time.Millisecond)

	// Pre-verify all 3 tunnels work before killing B.
	for i, info := range []struct {
		name string
		resp protocol.TunnelResp
	}{{"A", trespA}, {"B", trespB}, {"C", trespC}} {
		msg := []byte(fmt.Sprintf("pre-kill-%s-%d", info.name, i))
		if err := mc_doEchoRoundTrip(info.resp.PublicPort, msg); err != nil {
			t.Fatalf("SC2: pre-kill verification of client %s failed: %v", info.name, err)
		}
	}

	// Kill client B's control connection abruptly.
	t.Log("SC2: killing client B control connection")
	connB.Close()

	// Wait for the server to detect disconnection and clean up B's tunnels
	// (poll up to 1 second).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(infra.reg.List()) == 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Give cleanup a moment more.
	time.Sleep(50 * time.Millisecond)

	// Client A must still work.
	if err := mc_doEchoRoundTrip(trespA.PublicPort, []byte("post-kill-A")); err != nil {
		t.Errorf("SC2: client A tunnel failed after client B was killed: %v", err)
	}

	// Client C must still work.
	if err := mc_doEchoRoundTrip(trespC.PublicPort, []byte("post-kill-C")); err != nil {
		t.Errorf("SC2: client C tunnel failed after client B was killed: %v", err)
	}

	// Exactly 2 clients should remain.
	remaining := infra.reg.List()
	if len(remaining) != 2 {
		t.Errorf("SC2: expected 2 clients after killing B, got %d", len(remaining))
	}

	// Manager must not have B's tunnel anymore.
	if _, ok := infra.mgr.GetTunnel(trespB.TunnelID); ok {
		t.Errorf("SC2: client B's tunnel %q still in manager after disconnect", trespB.TunnelID)
	}

	// Manager must still have A's and C's tunnels.
	if _, ok := infra.mgr.GetTunnel(trespA.TunnelID); !ok {
		t.Errorf("SC2: client A's tunnel %q missing from manager after B was killed", trespA.TunnelID)
	}
	if _, ok := infra.mgr.GetTunnel(trespC.TunnelID); !ok {
		t.Errorf("SC2: client C's tunnel %q missing from manager after B was killed", trespC.TunnelID)
	}

	connA.Close()
	connC.Close()
}

// TestSC2_HTTPClientFailureIsolation kills one of three HTTP clients and
// verifies the remaining HTTP tunnels still route correctly.
func TestSC2_HTTPClientFailureIsolation(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	portA := startLocalHTTP(t, ctx, "http-A-alive")
	portB := startLocalHTTP(t, ctx, "http-B-dead")
	portC := startLocalHTTP(t, ctx, "http-C-alive")

	connA, hrespA := connectHTTPClient(t, infra, "http-a.mc.local", "127.0.0.1", portA)
	go runHTTPClientLoop(connA, map[string]string{hrespA.TunnelID: hrespA.ServerDataAddr})

	connB, hrespB := connectHTTPClient(t, infra, "http-b.mc.local", "127.0.0.1", portB)
	go runHTTPClientLoop(connB, map[string]string{hrespB.TunnelID: hrespB.ServerDataAddr})

	connC, hrespC := connectHTTPClient(t, infra, "http-c.mc.local", "127.0.0.1", portC)
	go runHTTPClientLoop(connC, map[string]string{hrespC.TunnelID: hrespC.ServerDataAddr})
	_ = hrespC

	time.Sleep(80 * time.Millisecond)

	// Verify pre-kill state.
	for _, tc := range []struct{ host, want string }{
		{"http-a.mc.local", "http-A-alive"},
		{"http-b.mc.local", "http-B-dead"},
		{"http-c.mc.local", "http-C-alive"},
	} {
		body := doHTTPRequest(t, infra.httpAddr, tc.host, "/")
		if !mc_contains(body, tc.want) {
			t.Fatalf("SC2-HTTP pre-kill: host %q: want %q, got %q", tc.host, tc.want, body)
		}
	}

	// Kill client B.
	t.Log("SC2-HTTP: killing client B")
	connB.Close()

	// Wait for server-side cleanup.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := infra.mgr.GetHTTPTunnel("http-b.mc.local"); !ok {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	time.Sleep(30 * time.Millisecond)

	// A and C must still respond correctly.
	bodyA := doHTTPRequest(t, infra.httpAddr, "http-a.mc.local", "/")
	if !mc_contains(bodyA, "http-A-alive") {
		t.Errorf("SC2-HTTP: client A failed after B killed: got %q", bodyA)
	}
	bodyC := doHTTPRequest(t, infra.httpAddr, "http-c.mc.local", "/")
	if !mc_contains(bodyC, "http-C-alive") {
		t.Errorf("SC2-HTTP: client C failed after B killed: got %q", bodyC)
	}

	// B's hostname must be gone from the manager.
	if _, ok := infra.mgr.GetHTTPTunnel("http-b.mc.local"); ok {
		t.Errorf("SC2-HTTP: client B's hostname 'http-b.mc.local' still in manager after disconnect")
	}

	connA.Close()
	connC.Close()
}

// ---------------------------------------------------------------------------
// SC3: Concurrent load
// ---------------------------------------------------------------------------

// TestSC3_ConcurrentLoad connects 3 clients and sends concurrent echo requests
// through all 3 tunnels simultaneously.  Verifies correctness and that average
// RTT is below 100 ms on loopback.
func TestSC3_ConcurrentLoad(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		numClients            = 3
		goroutinesPerTunnel   = 8
		roundTripsPerGoroutine = 5
	)

	type tunnelSlot struct {
		publicPort int
	}

	slots := make([]tunnelSlot, numClients)
	ctrlConns := make([]net.Conn, numClients)

	for i := 0; i < numClients; i++ {
		echoPort := startLocalEcho(t, ctx)

		conn, _, tresp := mc_connectTCP(
			t, infra.dialFn,
			fmt.Sprintf("load-%d", i),
			"127.0.0.1", echoPort,
		)
		ctrlConns[i] = conn
		slots[i].publicPort = tresp.PublicPort

		go runClientMessageLoop(conn, map[string]string{tresp.TunnelID: tresp.ServerDataAddr}, nil)
	}

	defer func() {
		for _, c := range ctrlConns {
			c.Close()
		}
	}()

	time.Sleep(80 * time.Millisecond)

	var (
		wg          sync.WaitGroup
		errCount    atomic.Int64
		totalNS     atomic.Int64
		sampleCount atomic.Int64
	)

	for clientIdx := 0; clientIdx < numClients; clientIdx++ {
		port := slots[clientIdx].publicPort

		for g := 0; g < goroutinesPerTunnel; g++ {
			wg.Add(1)
			go func(cIdx, gIdx int) {
				defer wg.Done()

				conn, err := net.DialTimeout("tcp",
					fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
				if err != nil {
					t.Logf("SC3: client %d goroutine %d: dial: %v", cIdx, gIdx, err)
					errCount.Add(1)
					return
				}
				defer conn.Close()

				for r := 0; r < roundTripsPerGoroutine; r++ {
					msg := []byte(fmt.Sprintf("load-c%d-g%d-r%d", cIdx, gIdx, r))

					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

					start := time.Now()
					if _, err := conn.Write(msg); err != nil {
						t.Logf("SC3: client %d goroutine %d write: %v", cIdx, gIdx, err)
						errCount.Add(1)
						return
					}

					buf := make([]byte, len(msg))
					if _, err := io.ReadFull(conn, buf); err != nil {
						t.Logf("SC3: client %d goroutine %d read: %v", cIdx, gIdx, err)
						errCount.Add(1)
						return
					}

					rtt := time.Since(start)

					if !bytes.Equal(buf, msg) {
						t.Logf("SC3: data mismatch: got %q, want %q", buf, msg)
						errCount.Add(1)
						return
					}

					totalNS.Add(int64(rtt))
					sampleCount.Add(1)
				}
			}(clientIdx, g)
		}
	}

	wg.Wait()

	totalErrors := errCount.Load()
	samples := sampleCount.Load()

	if totalErrors > 0 {
		t.Errorf("SC3: %d errors during concurrent load", totalErrors)
	}
	if samples == 0 {
		t.Fatal("SC3: no successful samples collected")
	}

	avgRTT := time.Duration(totalNS.Load() / samples)
	t.Logf("SC3: %d samples across %d tunnels, avg RTT = %v, errors = %d",
		samples, numClients, avgRTT, totalErrors)

	const maxAvgRTT = 100 * time.Millisecond
	if avgRTT > maxAvgRTT {
		t.Errorf("SC3: average RTT %v exceeds threshold %v", avgRTT, maxAvgRTT)
	}
}

// TestSC3_ConcurrentHTTPLoad sends concurrent HTTP requests through 3 different
// HTTP tunnels simultaneously and verifies each response routes correctly.
func TestSC3_ConcurrentHTTPLoad(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numClients = 3
	const requestsPerClient = 2

	type httpSlot struct {
		host string
		want string
	}

	slots := make([]httpSlot, numClients)
	ctrlConns := make([]net.Conn, numClients)

	for i := 0; i < numClients; i++ {
		host := fmt.Sprintf("load-http-%d.local", i)
		body := fmt.Sprintf("http-body-%d", i)
		port := startLocalHTTP(t, ctx, body)

		conn, hresp := connectHTTPClient(t, infra, host, "127.0.0.1", port)
		ctrlConns[i] = conn
		go runHTTPClientLoop(conn, map[string]string{hresp.TunnelID: hresp.ServerDataAddr})

		slots[i] = httpSlot{host: host, want: body}
	}

	defer func() {
		for _, c := range ctrlConns {
			c.Close()
		}
	}()

	time.Sleep(80 * time.Millisecond)

	var wg sync.WaitGroup
	var errCount atomic.Int64

	for i := 0; i < numClients; i++ {
		slot := slots[i]
		for r := 0; r < requestsPerClient; r++ {
			wg.Add(1)
			go func(s httpSlot) {
				defer wg.Done()
				got, err := mc_doHTTPRequest(infra.httpAddr, s.host, "/")
				if err != nil {
					t.Logf("SC3-HTTP: host %q: request error: %v", s.host, err)
					errCount.Add(1)
					return
				}
				if !mc_contains(got, s.want) {
					t.Logf("SC3-HTTP: host %q: want %q, got %q", s.host, s.want, got)
					errCount.Add(1)
				}
			}(slot)
		}
	}

	wg.Wait()

	if n := errCount.Load(); n > 0 {
		t.Errorf("SC3-HTTP: %d routing errors during concurrent HTTP load", n)
	}
}

// ---------------------------------------------------------------------------
// Phase 5 private helpers
// ---------------------------------------------------------------------------

// mc_connectTCP starts a client-side TLS listener, has the server dial it,
// performs the reversed handshake, and requests a TCP tunnel to localHost:localPort.
func mc_connectTCP(
	t *testing.T,
	dialFn func(string),
	name, localHost string,
	localPort int,
) (net.Conn, string, protocol.TunnelResp) {
	t.Helper()

	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "client.crt"),
		filepath.Join(dir, "client.key"),
	)
	if err != nil {
		t.Fatalf("mc_connectTCP %q: LoadOrGenerateCert: %v", name, err)
	}

	tlsCfg := control.NewServerTLSConfig(cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("mc_connectTCP %q: tls.Listen: %v", name, err)
	}
	clientAddr := ln.Addr().String()

	// Server dials the client.
	dialFn(clientAddr)

	// Accept the server connection (with timeout).
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acCh := make(chan acceptResult, 1)
	go func() {
		c, e := ln.Accept()
		acCh <- acceptResult{c, e}
	}()
	time.AfterFunc(5*time.Second, func() { _ = ln.Close() })
	ar := <-acCh
	_ = ln.Close()
	conn, err := ar.conn, ar.err
	if err != nil {
		t.Fatalf("mc_connectTCP %q: Accept: %v", name, err)
	}

	// Client-side handshake.
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: read ClientRegister: %v", name, err)
	}
	if env.Type != protocol.MsgClientRegister {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: expected MsgClientRegister, got %v", name, env.Type)
	}
	var regMsg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&regMsg); err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: decode ClientRegister: %v", name, err)
	}
	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: name,
	}); err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: write RegisterResp: %v", name, err)
	}

	// Allow server goroutine to process registration.
	time.Sleep(50 * time.Millisecond)

	if localPort == 0 {
		return conn, "", protocol.TunnelResp{}
	}

	if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, protocol.RequestTunnel{
		LocalHost:     localHost,
		LocalPort:     localPort,
		RequestedPort: 0,
	}); err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: write RequestTunnel: %v", name, err)
	}

	tenv, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: read TunnelResp: %v", name, err)
	}
	if tenv.Type != protocol.MsgTunnelResp {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: expected MsgTunnelResp, got %v", name, tenv.Type)
	}
	var tresp protocol.TunnelResp
	if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: decode TunnelResp: %v", name, err)
	}
	if tresp.Status != "ok" {
		conn.Close()
		t.Fatalf("mc_connectTCP %q: tunnel request failed: %s", name, tresp.Error)
	}

	return conn, "", tresp
}

// mc_doEchoRoundTrip dials publicPort, sends msg as a full write+CloseWrite,
// reads back the echo, and verifies byte-for-byte equality.
func mc_doEchoRoundTrip(publicPort int, msg []byte) error {
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", publicPort), 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial port %d: %w", publicPort, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		return fmt.Errorf("CloseWrite: %w", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if !bytes.Equal(buf, msg) {
		return fmt.Errorf("data mismatch: got %q, want %q", buf, msg)
	}
	return nil
}

// mc_contains returns true if s contains the substring sub.
func mc_contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// mc_doHTTPRequest sends a plain HTTP GET to proxyAddr with the given Host
// header and returns the raw response string.
func mc_doHTTPRequest(proxyAddr, host, path string) (string, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", proxyAddr, err)
	}
	defer conn.Close()

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	raw, err := io.ReadAll(conn)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(raw), nil
}

// Ensure tunnel package is used.
var _ = tunnel.NewManager
