package control_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/gob"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
)

// startTestServer starts an in-process TLS server that accepts control
// connections. It returns the server's local address, the client registry,
// and a shutdown function that cancels the server context and closes the
// listener.
func startTestServer(t *testing.T, token string) (addr string, reg *control.ClientRegistry, shutdown func()) {
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

	reg = control.NewClientRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	log := logger.New("test-server")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				// Listener closed — normal shutdown.
				return
			}
			go control.HandleControlConn(ctx, conn, reg, token, log)
		}
	}()

	shutdown = func() {
		cancel()
		ln.Close()
	}

	return ln.Addr().String(), reg, shutdown
}

// connectTestClient dials the server, performs the registration handshake,
// and returns the open connection along with the assigned client ID.
// The caller is responsible for closing the connection.
func connectTestClient(t *testing.T, addr, token, name string) (net.Conn, string) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true) // InsecureSkipVerify for self-signed cert
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}

	// Send ClientRegister.
	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: token,
		Name:      name,
		Version:   "0.1.0",
	}); err != nil {
		conn.Close()
		t.Fatalf("WriteMessage ClientRegister: %v", err)
	}

	// Read RegisterResp.
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("ReadMessage RegisterResp: %v", err)
	}

	var resp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&resp); err != nil {
		conn.Close()
		t.Fatalf("decode RegisterResp: %v", err)
	}

	if resp.Status != "ok" {
		conn.Close()
		t.Fatalf("registration failed: %s", resp.Error)
	}

	return conn, resp.AssignedClientID
}

// TestSC1_ClientRegistration verifies SC1:
// When a client connects to the proxy server, client registration is confirmed
// in the server registry.
func TestSC1_ClientRegistration(t *testing.T) {
	addr, reg, shutdown := startTestServer(t, "secret")
	defer shutdown()

	conn, _ := connectTestClient(t, addr, "secret", "test-client")
	defer conn.Close()

	// Allow the server goroutine time to add the client to the registry.
	time.Sleep(100 * time.Millisecond)

	clients := reg.List()
	if len(clients) != 1 {
		t.Fatalf("expected 1 registered client, got %d", len(clients))
	}
	if clients[0].Name != "test-client" {
		t.Errorf("expected client name %q, got %q", "test-client", clients[0].Name)
	}
}

// TestSC2_MultipleClients verifies SC2:
// Multiple clients connect simultaneously; each is independently identified
// with a unique UUID in the registry.
func TestSC2_MultipleClients(t *testing.T) {
	addr, reg, shutdown := startTestServer(t, "secret")
	defer shutdown()

	conn1, _ := connectTestClient(t, addr, "secret", "c1")
	defer conn1.Close()

	conn2, _ := connectTestClient(t, addr, "secret", "c2")
	defer conn2.Close()

	// Allow server goroutines to finish registration.
	time.Sleep(200 * time.Millisecond)

	clients := reg.List()
	if len(clients) != 2 {
		t.Fatalf("expected 2 registered clients, got %d", len(clients))
	}
	if clients[0].ID == clients[1].ID {
		t.Errorf("expected unique IDs for each client, both got %q", clients[0].ID)
	}
}

// TestSC3_DisconnectionDetected verifies SC3:
// When a client disconnects, the server detects the disconnection and removes
// it from the registry.
func TestSC3_DisconnectionDetected(t *testing.T) {
	addr, reg, shutdown := startTestServer(t, "secret")
	defer shutdown()

	conn, id := connectTestClient(t, addr, "secret", "disconnect-client")

	// Allow the server to complete registration.
	time.Sleep(100 * time.Millisecond)

	// Verify the client is registered.
	if _, ok := reg.Get(id); !ok {
		t.Fatalf("client %q not in registry after connect", id)
	}

	// Close the connection abruptly — server should detect EOF.
	conn.Close()

	// Poll the registry for up to 500 ms to confirm deregistration.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get(id); !ok {
			return // deregistered — test passes
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Errorf("client %q still present in registry 500 ms after disconnect", id)
}
