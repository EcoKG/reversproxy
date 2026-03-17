package tunnel_test

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
	"testing"
	"time"

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Test helpers — new connection direction
//
// In the new architecture:
//   - The CLIENT listens (TLS) and waits for the SERVER to connect.
//   - The SERVER dials the client, calls HandleControlConn.
//
// In our tests:
//   - testInfra is the SERVER side: has registry, tunnel manager, data listener,
//     and a goroutine that dials client listeners.
//   - connectClient starts a client-side TLS listener, accepts the server
//     connection, performs the reversed handshake, then optionally requests
//     a tunnel, and returns everything the test needs.
// ---------------------------------------------------------------------------

// testInfra holds all the pieces of an in-process test server setup.
type testInfra struct {
	reg            *control.ClientRegistry
	mgr            *tunnel.Manager
	serverDataAddr string
	// dialFn is called by the server to connect to a client listener address.
	dialFn  func(clientAddr string)
	shutdown func()
	ctx      context.Context
	log      interface{ Info(string, ...any); Error(string, ...any) }
}

// startInfra creates:
//   - A tunnel data listener (tunnel.StartDataListener) on the SERVER side.
//   - A client registry and manager.
//   - A dialFn that the server uses to connect to a given client address.
//
// It returns the testInfra and a shutdown function.
func startInfra(t *testing.T) *testInfra {
	t.Helper()

	reg := tunnel.NewManager()
	clientReg := control.NewClientRegistry()
	log := logger.New("test-server")

	ctx, cancel := context.WithCancel(context.Background())

	// Start data listener on a random port.
	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", reg, log); err != nil {
		cancel()
		t.Fatalf("StartDataListener: %v", err)
	}
	dataAddr := tunnel.DataAddr

	tlsCfg := control.NewClientTLSConfig(true) // server dials with InsecureSkipVerify

	dialFn := func(clientAddr string) {
		go func() {
			conn, err := tls.Dial("tcp", clientAddr, tlsCfg)
			if err != nil {
				return
			}
			control.HandleControlConn(ctx, conn, clientReg, "secret", log, reg, dataAddr)
		}()
	}

	shutdown := func() {
		cancel()
	}

	return &testInfra{
		reg:            clientReg,
		mgr:            reg,
		serverDataAddr: dataAddr,
		dialFn:         dialFn,
		shutdown:       shutdown,
		ctx:            ctx,
	}
}

// connectClient starts a TLS client listener, signals the server to dial it,
// performs the client-side handshake, and optionally requests a tunnel.
// Returns the open control conn (server side of the pipe from server's perspective,
// but actually the conn accepted by the client listener), clientID, and tunnelResp.
func connectClient(
	t *testing.T,
	infra *testInfra,
	localHost string,
	localPort int,
) (ctrlConn net.Conn, clientID string, tunnelResp protocol.TunnelResp) {
	t.Helper()

	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "client.crt"),
		filepath.Join(dir, "client.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}

	clientTLSCfg := control.NewServerTLSConfig(cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", clientTLSCfg)
	if err != nil {
		t.Fatalf("client tls.Listen: %v", err)
	}
	clientAddr := ln.Addr().String()

	// Signal the server to dial this client.
	infra.dialFn(clientAddr)

	// Accept the server's connection on the client side (with timeout).
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
	_ = ln.Close() // one-shot; close after accepting.
	conn, err := ar.conn, ar.err
	if err != nil {
		t.Fatalf("client Accept: %v", err)
	}

	// Client-side handshake: read ClientRegister, validate, send RegisterResp.
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("read ClientRegister: %v", err)
	}
	if env.Type != protocol.MsgClientRegister {
		conn.Close()
		t.Fatalf("expected MsgClientRegister, got %v", env.Type)
	}
	var reg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&reg); err != nil {
		conn.Close()
		t.Fatalf("decode ClientRegister: %v", err)
	}
	if reg.AuthToken != "secret" {
		conn.Close()
		t.Fatalf("invalid token from server: %q", reg.AuthToken)
	}

	// Send RegisterResp — include this client's name as ServerID.
	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: "test-client",
	}); err != nil {
		conn.Close()
		t.Fatalf("WriteMessage RegisterResp: %v", err)
	}

	// Allow server goroutine to process registration.
	time.Sleep(50 * time.Millisecond)

	// Find the assigned clientID from the server registry.
	clients := infra.reg.List()
	if len(clients) > 0 {
		clientID = clients[len(clients)-1].ID
	}

	if localPort == 0 {
		return conn, clientID, protocol.TunnelResp{}
	}

	// Request tunnel by sending RequestTunnel through the client-side conn.
	// The server's HandleControlConn reads this and creates a public listener.
	if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, protocol.RequestTunnel{
		LocalHost:     localHost,
		LocalPort:     localPort,
		RequestedPort: 0,
	}); err != nil {
		conn.Close()
		t.Fatalf("WriteMessage RequestTunnel: %v", err)
	}

	tenv, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("ReadMessage TunnelResp: %v", err)
	}
	if tenv.Type != protocol.MsgTunnelResp {
		conn.Close()
		t.Fatalf("expected MsgTunnelResp, got %v", tenv.Type)
	}
	if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tunnelResp); err != nil {
		conn.Close()
		t.Fatalf("decode TunnelResp: %v", err)
	}
	if tunnelResp.Status != "ok" {
		conn.Close()
		t.Fatalf("tunnel request failed: %s", tunnelResp.Error)
	}

	return conn, clientID, tunnelResp
}

// startLocalEcho starts a local TCP echo server on an OS-assigned port.
// It returns the port. The server stops when ctx is cancelled.
func startLocalEcho(t *testing.T, ctx context.Context) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startLocalEcho listen: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port

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
				_, _ = io.Copy(c, c) // echo
			}(conn)
		}
	}()

	return port
}

// runClientMessageLoop reads OpenConnection messages from ctrlConn and calls
// HandleOpenConnection for each one. It runs until ctrlConn is closed.
func runClientMessageLoop(ctrlConn net.Conn, tunnelDataAddrs map[string]string, log interface{ Error(...interface{}) }) {
	for {
		env, err := protocol.ReadMessage(ctrlConn)
		if err != nil {
			return
		}
		if env.Type == protocol.MsgOpenConnection {
			var openConn protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&openConn); err != nil {
				continue
			}
			dataAddr, ok := tunnelDataAddrs[openConn.TunnelID]
			if !ok {
				continue
			}
			testLog := logger.New("test-client")
			tunnel.HandleOpenConnection(openConn, dataAddr, testLog)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSC1_ExternalConnectionReachesLocalService verifies Success Criterion 1:
// An external user connecting to the proxy's public port can communicate with
// the service running behind the client.
func TestSC1_ExternalConnectionReachesLocalService(t *testing.T) {
	infra := startInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a local echo service.
	echoPort := startLocalEcho(t, ctx)

	// Server dials client and client requests a tunnel to the echo service.
	ctrlConn, _, tunnelResp := connectClient(t, infra, "127.0.0.1", echoPort)
	defer ctrlConn.Close()

	// Run the client message loop in the background.
	tunnelDataAddrs := map[string]string{tunnelResp.TunnelID: tunnelResp.ServerDataAddr}
	go runClientMessageLoop(ctrlConn, tunnelDataAddrs, nil)

	// Allow listeners to settle.
	time.Sleep(50 * time.Millisecond)

	// External user connects to the public port.
	extConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelResp.PublicPort))
	if err != nil {
		t.Fatalf("external user dial: %v", err)
	}
	defer extConn.Close()

	// Send a message and expect it echoed back.
	msg := []byte("hello via tunnel")
	if _, err := extConn.Write(msg); err != nil {
		t.Fatalf("external user write: %v", err)
	}

	_ = extConn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(extConn, buf); err != nil {
		t.Fatalf("external user read: %v", err)
	}

	if !bytes.Equal(buf, msg) {
		t.Errorf("echo mismatch: got %q, want %q", buf, msg)
	}
}

// TestSC2_DataIntegrity verifies Success Criterion 2:
// Data transmitted through the tunnel arrives without loss or corruption,
// in both directions.
func TestSC2_DataIntegrity(t *testing.T) {
	infra := startInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoPort := startLocalEcho(t, ctx)

	ctrlConn, _, tunnelResp := connectClient(t, infra, "127.0.0.1", echoPort)
	defer ctrlConn.Close()

	tunnelDataAddrs := map[string]string{tunnelResp.TunnelID: tunnelResp.ServerDataAddr}
	go runClientMessageLoop(ctrlConn, tunnelDataAddrs, nil)

	time.Sleep(50 * time.Millisecond)

	extConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelResp.PublicPort))
	if err != nil {
		t.Fatalf("external user dial: %v", err)
	}
	defer extConn.Close()

	// Build a 64 KB payload to test large data transfer.
	const size = 64 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251) // prime modulus for non-trivial pattern
	}

	// Write full payload then half-close.
	go func() {
		_, _ = extConn.Write(payload)
		_ = extConn.(*net.TCPConn).CloseWrite()
	}()

	received := make([]byte, size)
	if _, err := io.ReadFull(extConn, received); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	if !bytes.Equal(received, payload) {
		// Find first mismatch byte.
		for i := range payload {
			if received[i] != payload[i] {
				t.Errorf("data corruption at byte %d: got %d, want %d", i, received[i], payload[i])
				break
			}
		}
	}
}

// TestSC3_ConcurrentConnections verifies Success Criterion 3:
// Multiple simultaneous external TCP connections through a single tunnel all
// work correctly without interfering with each other.
func TestSC3_ConcurrentConnections(t *testing.T) {
	infra := startInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoPort := startLocalEcho(t, ctx)

	ctrlConn, _, tunnelResp := connectClient(t, infra, "127.0.0.1", echoPort)
	defer ctrlConn.Close()

	tunnelDataAddrs := map[string]string{tunnelResp.TunnelID: tunnelResp.ServerDataAddr}
	go runClientMessageLoop(ctrlConn, tunnelDataAddrs, nil)

	time.Sleep(50 * time.Millisecond)

	const numConns = 5
	var wg sync.WaitGroup
	errors := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelResp.PublicPort))
			if err != nil {
				errors <- fmt.Errorf("conn %d dial: %w", id, err)
				return
			}
			defer conn.Close()

			// Each goroutine sends a unique message and expects the same back.
			msg := []byte(fmt.Sprintf("concurrent-message-%d", id))
			go func() {
				_, _ = conn.Write(msg)
				_ = conn.(*net.TCPConn).CloseWrite()
			}()

			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errors <- fmt.Errorf("conn %d read: %w", id, err)
				return
			}

			if !bytes.Equal(buf, msg) {
				errors <- fmt.Errorf("conn %d data mismatch: got %q, want %q", id, buf, msg)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Errorf("concurrent connection error: %v", err)
		}
	}
}
