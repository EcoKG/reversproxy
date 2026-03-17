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
// Test helpers
// ---------------------------------------------------------------------------

// testInfra holds all the pieces of an in-process test server+client setup.
type testInfra struct {
	reg            *control.ClientRegistry
	mgr            *tunnel.Manager
	serverDataAddr string
	controlAddr    string
	shutdown       func()
}

// startInfra creates:
//   - A TLS control server (control.HandleControlConn)
//   - A tunnel data listener (tunnel.StartDataListener)
//
// It returns the testInfra and a shutdown function.
func startInfra(t *testing.T) *testInfra {
	t.Helper()

	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "server.crt"),
		filepath.Join(dir, "server.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}

	tlsCfg := control.NewServerTLSConfig(cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}

	reg := control.NewClientRegistry()
	mgr := tunnel.NewManager()
	log := logger.New("test-server")

	ctx, cancel := context.WithCancel(context.Background())

	// Start data listener on a random port.
	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", mgr, log); err != nil {
		cancel()
		t.Fatalf("StartDataListener: %v", err)
	}
	dataAddr := tunnel.DataAddr

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go control.HandleControlConn(ctx, conn, reg, "secret", log, mgr, dataAddr)
		}
	}()

	shutdown := func() {
		cancel()
		_ = ln.Close()
	}

	return &testInfra{
		reg:            reg,
		mgr:            mgr,
		serverDataAddr: dataAddr,
		controlAddr:    ln.Addr().String(),
		shutdown:       shutdown,
	}
}

// connectClient dials the control server, registers, and optionally requests a
// tunnel for localHost:localPort (skip if localPort == 0).
// It returns the open control conn, the assigned client ID, and tunnel info.
func connectClient(
	t *testing.T,
	controlAddr string,
	localHost string,
	localPort int,
) (ctrlConn net.Conn, clientID string, tunnelResp protocol.TunnelResp) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true)
	conn, err := tls.Dial("tcp", controlAddr, tlsCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}

	// Register.
	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: "secret",
		Name:      "test-client",
		Version:   "0.1.0",
	}); err != nil {
		conn.Close()
		t.Fatalf("WriteMessage ClientRegister: %v", err)
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("ReadMessage RegisterResp: %v", err)
	}
	var regResp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&regResp); err != nil {
		conn.Close()
		t.Fatalf("decode RegisterResp: %v", err)
	}
	if regResp.Status != "ok" {
		conn.Close()
		t.Fatalf("registration failed: %s", regResp.Error)
	}
	clientID = regResp.AssignedClientID

	if localPort == 0 {
		return conn, clientID, protocol.TunnelResp{}
	}

	// Request tunnel.
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
	// Use a simple stderr-based logger substitute; for tests we ignore log.
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

	// Connect client and request a tunnel to the echo service.
	ctrlConn, _, tunnelResp := connectClient(t, infra.controlAddr, "127.0.0.1", echoPort)
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

	ctrlConn, _, tunnelResp := connectClient(t, infra.controlAddr, "127.0.0.1", echoPort)
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

	ctrlConn, _, tunnelResp := connectClient(t, infra.controlAddr, "127.0.0.1", echoPort)
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
