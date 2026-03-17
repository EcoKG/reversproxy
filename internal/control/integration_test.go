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

	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
)

// ---------------------------------------------------------------------------
// Test infrastructure — new connection direction
//
// In the new architecture:
//   - The CLIENT listens (tls.Listen) and waits for the server.
//   - The SERVER dials (tls.Dial) to each client.
//
// For testing we simulate this by:
//   1. startTestClient: starts a TLS listener that acts as the "client".
//      It accepts a connection, performs the new handshake (reads ClientRegister
//      from the server, validates the token, sends RegisterResp), and then
//      hands the connection to a goroutine that drains messages.
//   2. connectTestServer: the "server" side dials the client listener and calls
//      HandleControlConn, which now sends ClientRegister and reads RegisterResp.
// ---------------------------------------------------------------------------

// testClientListener simulates a client that listens for server connections.
// It returns the listener address, the underlying net.Listener, and an error
// channel that receives any handshake failures.
func startTestClient(t *testing.T, token string) (addr string, ln net.Listener, accepted chan net.Conn) {
	t.Helper()

	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "client.crt"),
		filepath.Join(dir, "client.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}

	tlsCfg := control.NewServerTLSConfig(cert)
	l, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls.Listen (client): %v", err)
	}

	accepted = make(chan net.Conn, 4)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed
			}
			// Perform client-side handshake: read ClientRegister, validate, send RegisterResp.
			go func(c net.Conn) {
				env, err := protocol.ReadMessage(c)
				if err != nil || env.Type != protocol.MsgClientRegister {
					c.Close()
					return
				}
				var reg protocol.ClientRegister
				if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&reg); err != nil {
					c.Close()
					return
				}
				if reg.AuthToken != token {
					_ = protocol.WriteMessage(c, protocol.MsgRegisterResp, protocol.RegisterResp{
						Status: "error",
						Error:  "invalid token",
					})
					c.Close()
					return
				}
				_ = protocol.WriteMessage(c, protocol.MsgRegisterResp, protocol.RegisterResp{
					Status:   "ok",
					ServerID: "test-client", // client's name conveyed back
				})
				accepted <- c
			}(conn)
		}
	}()

	return l.Addr().String(), l, accepted
}

// startTestServer starts an in-process server that dials the client at clientAddr.
// It returns the client registry and a shutdown function.
func startTestServer(t *testing.T, clientAddr, token string) (reg *control.ClientRegistry, shutdown func()) {
	t.Helper()

	reg = control.NewClientRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	log := logger.New("test-server")

	// Server dials the client using InsecureSkipVerify (self-signed cert).
	tlsCfg := control.NewClientTLSConfig(true)

	go func() {
		conn, err := tls.Dial("tcp", clientAddr, tlsCfg)
		if err != nil {
			return
		}
		control.HandleControlConn(ctx, conn, reg, token, log, nil, "")
	}()

	shutdown = func() {
		cancel()
	}
	return reg, shutdown
}

// TestSC1_ClientRegistration verifies SC1:
// When the server dials the client, registration is confirmed in the server registry.
func TestSC1_ClientRegistration(t *testing.T) {
	clientAddr, ln, _ := startTestClient(t, "secret")
	defer ln.Close()

	reg, shutdown := startTestServer(t, clientAddr, "secret")
	defer shutdown()

	// Allow the server goroutine time to complete the handshake.
	time.Sleep(200 * time.Millisecond)

	clients := reg.List()
	if len(clients) != 1 {
		t.Fatalf("expected 1 registered client, got %d", len(clients))
	}
	if clients[0].Name != "test-client" {
		t.Errorf("expected client name %q, got %q", "test-client", clients[0].Name)
	}
}

// TestSC2_MultipleClients verifies SC2:
// Multiple server connections to different clients register each independently
// with a unique UUID.
func TestSC2_MultipleClients(t *testing.T) {
	// Two separate client listeners.
	addr1, ln1, _ := startTestClient(t, "secret")
	defer ln1.Close()

	addr2, ln2, _ := startTestClient(t, "secret")
	defer ln2.Close()

	reg := control.NewClientRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := logger.New("test-server")
	tlsCfg := control.NewClientTLSConfig(true)

	// Server dials both clients.
	for _, addr := range []string{addr1, addr2} {
		a := addr
		go func() {
			conn, err := tls.Dial("tcp", a, tlsCfg)
			if err != nil {
				return
			}
			control.HandleControlConn(ctx, conn, reg, "secret", log, nil, "")
		}()
	}

	// Allow server goroutines to finish registration.
	time.Sleep(300 * time.Millisecond)

	clients := reg.List()
	if len(clients) != 2 {
		t.Fatalf("expected 2 registered clients, got %d", len(clients))
	}
	if clients[0].ID == clients[1].ID {
		t.Errorf("expected unique IDs for each client, both got %q", clients[0].ID)
	}
}

// TestSC3_DisconnectionDetected verifies SC3:
// When the client closes the connection, the server detects the disconnection
// and removes it from the registry.
func TestSC3_DisconnectionDetected(t *testing.T) {
	clientAddr, ln, accepted := startTestClient(t, "secret")
	defer ln.Close()

	reg, shutdown := startTestServer(t, clientAddr, "secret")
	defer shutdown()

	// Allow the server to complete registration.
	time.Sleep(200 * time.Millisecond)

	clients := reg.List()
	if len(clients) != 1 {
		t.Fatalf("expected 1 registered client, got %d", len(clients))
	}
	id := clients[0].ID

	// Close the connection from the client side.
	select {
	case conn := <-accepted:
		conn.Close()
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for accepted connection on client side")
	}

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
