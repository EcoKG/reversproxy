package tunnel_test

// HTTP/HTTPS host-based routing integration tests — Phase 3.
//
// Success Criteria:
//   SC1: Different hostnames route HTTP requests to the correct client tunnel.
//   SC2: HTTPS requests are routed by SNI to the correct client tunnel.
//   SC3: Requests for unknown hosts return a clear error (502 / connection closed).

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/gob"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// HTTP routing test infrastructure
// ---------------------------------------------------------------------------

// httpTestInfra bundles the shared server resources for Phase 3 tests.
type httpTestInfra struct {
	reg            *control.ClientRegistry
	mgr            *tunnel.Manager
	ctrlConns      *tunnel.ControlConnRegistry
	serverDataAddr string
	controlAddr    string
	httpAddr       string
	httpsAddr      string
	shutdown       func()
}

// startHTTPInfra starts:
//   - A TLS control server.
//   - A data listener.
//   - An HTTP proxy listener (:0).
//   - An HTTPS SNI proxy listener (:0).
func startHTTPInfra(t *testing.T) *httpTestInfra {
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
	ctrlLn, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls.Listen control: %v", err)
	}

	reg       := control.NewClientRegistry()
	mgr       := tunnel.NewManager()
	ctrlConns := tunnel.NewControlConnRegistry()
	log       := logger.New("test-http-server")

	ctx, cancel := context.WithCancel(context.Background())

	// Data listener.
	if err := tunnel.StartDataListener(ctx, "127.0.0.1:0", mgr, log); err != nil {
		cancel()
		_ = ctrlLn.Close()
		t.Fatalf("StartDataListener: %v", err)
	}
	dataAddr := tunnel.DataAddr

	// HTTP proxy.
	if err := tunnel.StartHTTPProxy(ctx, "127.0.0.1:0", mgr, ctrlConns, dataAddr, log); err != nil {
		cancel()
		_ = ctrlLn.Close()
		t.Fatalf("StartHTTPProxy: %v", err)
	}
	httpAddr := tunnel.LastHTTPAddr

	// HTTPS SNI proxy.
	if err := tunnel.StartHTTPSProxy(ctx, "127.0.0.1:0", mgr, ctrlConns, dataAddr, log); err != nil {
		cancel()
		_ = ctrlLn.Close()
		t.Fatalf("StartHTTPSProxy: %v", err)
	}
	httpsAddr := tunnel.LastHTTPSAddr

	// Control accept loop.
	go func() {
		for {
			conn, err := ctrlLn.Accept()
			if err != nil {
				return
			}
			go control.HandleControlConn(ctx, conn, reg, "secret", log, mgr, dataAddr, ctrlConns)
		}
	}()

	shutdown := func() {
		cancel()
		_ = ctrlLn.Close()
	}

	return &httpTestInfra{
		reg:            reg,
		mgr:            mgr,
		ctrlConns:      ctrlConns,
		serverDataAddr: dataAddr,
		controlAddr:    ctrlLn.Addr().String(),
		httpAddr:       httpAddr,
		httpsAddr:      httpsAddr,
		shutdown:       shutdown,
	}
}

// connectHTTPClient registers a client and requests an HTTP tunnel for hostname.
// It returns the control connection and the HTTPTunnelResp.
func connectHTTPClient(
	t *testing.T,
	controlAddr string,
	hostname, localHost string,
	localPort int,
) (ctrlConn net.Conn, hresp protocol.HTTPTunnelResp) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true)
	conn, err := tls.Dial("tcp", controlAddr, tlsCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}

	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: "secret",
		Name:      "test-http-client-" + hostname,
		Version:   "0.1.0",
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("WriteMessage ClientRegister: %v", err)
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ReadMessage RegisterResp: %v", err)
	}
	var regResp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&regResp); err != nil {
		_ = conn.Close()
		t.Fatalf("decode RegisterResp: %v", err)
	}
	if regResp.Status != "ok" {
		_ = conn.Close()
		t.Fatalf("registration failed: %s", regResp.Error)
	}

	if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPTunnel, protocol.RequestHTTPTunnel{
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("WriteMessage RequestHTTPTunnel: %v", err)
	}

	henv, err := protocol.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ReadMessage HTTPTunnelResp: %v", err)
	}
	if henv.Type != protocol.MsgHTTPTunnelResp {
		_ = conn.Close()
		t.Fatalf("expected MsgHTTPTunnelResp, got %v", henv.Type)
	}
	if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
		_ = conn.Close()
		t.Fatalf("decode HTTPTunnelResp: %v", err)
	}
	if hresp.Status != "ok" {
		_ = conn.Close()
		t.Fatalf("HTTP tunnel request failed: %s", hresp.Error)
	}

	return conn, hresp
}

// connectHTTPSClient registers a client and requests an HTTPS SNI tunnel.
func connectHTTPSClient(
	t *testing.T,
	controlAddr string,
	hostname, localHost string,
	localPort int,
) (ctrlConn net.Conn, hresp protocol.HTTPTunnelResp) {
	t.Helper()

	tlsCfg := control.NewClientTLSConfig(true)
	conn, err := tls.Dial("tcp", controlAddr, tlsCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}

	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: "secret",
		Name:      "test-https-client-" + hostname,
		Version:   "0.1.0",
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("WriteMessage ClientRegister: %v", err)
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ReadMessage RegisterResp: %v", err)
	}
	var regResp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&regResp); err != nil {
		_ = conn.Close()
		t.Fatalf("decode RegisterResp: %v", err)
	}
	if regResp.Status != "ok" {
		_ = conn.Close()
		t.Fatalf("registration failed: %s", regResp.Error)
	}

	if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPSTunnel, protocol.RequestHTTPSTunnel{
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("WriteMessage RequestHTTPSTunnel: %v", err)
	}

	henv, err := protocol.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ReadMessage HTTPTunnelResp (HTTPS): %v", err)
	}
	if henv.Type != protocol.MsgHTTPTunnelResp {
		_ = conn.Close()
		t.Fatalf("expected MsgHTTPTunnelResp, got %v", henv.Type)
	}
	if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
		_ = conn.Close()
		t.Fatalf("decode HTTPTunnelResp (HTTPS): %v", err)
	}
	if hresp.Status != "ok" {
		_ = conn.Close()
		t.Fatalf("HTTPS tunnel request failed: %s", hresp.Error)
	}

	return conn, hresp
}

// startLocalHTTP starts a minimal HTTP server on a random port.
// Each request is answered with the provided body text.
func startLocalHTTP(t *testing.T, ctx context.Context, body string) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startLocalHTTP listen: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}),
	}

	go func() {
		defer ln.Close()
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		_ = srv.Serve(ln)
	}()

	return port
}

// startLocalTLSEcho starts a TLS echo server on a random port using a
// self-signed certificate for hostname.
func startLocalTLSEcho(t *testing.T, ctx context.Context, hostname string) (port int, clientTLSCfg *tls.Config) {
	t.Helper()

	cert, pool := generateSelfSignedCert(t, hostname)

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("startLocalTLSEcho listen: %v", err)
	}

	port = ln.Addr().(*net.TCPAddr).Port

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

	clientTLSCfg = &tls.Config{
		ServerName: hostname,
		RootCAs:    pool,
	}
	return port, clientTLSCfg
}

// runHTTPClientLoop handles OpenConnection messages for the HTTP/HTTPS client.
func runHTTPClientLoop(ctrlConn net.Conn, tunnelDataAddrs map[string]string) {
	log := logger.New("test-http-client")
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
			tunnel.HandleOpenConnection(openConn, dataAddr, log)
		}
	}
}

// generateSelfSignedCert creates a self-signed TLS certificate for hostname.
func generateSelfSignedCert(t *testing.T, hostname string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM  := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	return tlsCert, pool
}

// ---------------------------------------------------------------------------
// SC1: HTTP host-based routing
// ---------------------------------------------------------------------------

// TestSC1_HTTPHostRouting verifies that HTTP requests with different Host
// headers are delivered to the correct client tunnel and local service.
func TestSC1_HTTPHostRouting(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	portA := startLocalHTTP(t, ctx, "response-from-A")
	portB := startLocalHTTP(t, ctx, "response-from-B")

	ctrlA, hrespA := connectHTTPClient(t, infra.controlAddr, "host-a.local", "127.0.0.1", portA)
	defer ctrlA.Close()

	ctrlB, hrespB := connectHTTPClient(t, infra.controlAddr, "host-b.local", "127.0.0.1", portB)
	defer ctrlB.Close()

	addrsA := map[string]string{hrespA.TunnelID: hrespA.ServerDataAddr}
	addrsB := map[string]string{hrespB.TunnelID: hrespB.ServerDataAddr}

	go runHTTPClientLoop(ctrlA, addrsA)
	go runHTTPClientLoop(ctrlB, addrsB)

	// Allow registration to propagate.
	time.Sleep(50 * time.Millisecond)

	bodyA := doHTTPRequest(t, infra.httpAddr, "host-a.local", "/")
	if !strings.Contains(bodyA, "response-from-A") {
		t.Errorf("SC1: host-a.local: want 'response-from-A', got %q", bodyA)
	}

	bodyB := doHTTPRequest(t, infra.httpAddr, "host-b.local", "/")
	if !strings.Contains(bodyB, "response-from-B") {
		t.Errorf("SC1: host-b.local: want 'response-from-B', got %q", bodyB)
	}
}

// ---------------------------------------------------------------------------
// SC2: HTTPS SNI-based routing
// ---------------------------------------------------------------------------

// TestSC2_HTTPSRoutingBySNI verifies that HTTPS connections with different
// SNI values are routed to the correct client tunnel.
func TestSC2_HTTPSRoutingBySNI(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	portA, clientTLSA := startLocalTLSEcho(t, ctx, "tls-a.local")
	portB, clientTLSB := startLocalTLSEcho(t, ctx, "tls-b.local")

	ctrlA, hrespA := connectHTTPSClient(t, infra.controlAddr, "tls-a.local", "127.0.0.1", portA)
	defer ctrlA.Close()

	ctrlB, hrespB := connectHTTPSClient(t, infra.controlAddr, "tls-b.local", "127.0.0.1", portB)
	defer ctrlB.Close()

	addrsA := map[string]string{hrespA.TunnelID: hrespA.ServerDataAddr}
	addrsB := map[string]string{hrespB.TunnelID: hrespB.ServerDataAddr}

	go runHTTPClientLoop(ctrlA, addrsA)
	go runHTTPClientLoop(ctrlB, addrsB)

	time.Sleep(50 * time.Millisecond)

	msgA := doTLSEchoThrough(t, infra.httpsAddr, clientTLSA, "hello-to-A")
	if msgA != "hello-to-A" {
		t.Errorf("SC2: tls-a.local: want 'hello-to-A', got %q", msgA)
	}

	msgB := doTLSEchoThrough(t, infra.httpsAddr, clientTLSB, "hello-to-B")
	if msgB != "hello-to-B" {
		t.Errorf("SC2: tls-b.local: want 'hello-to-B', got %q", msgB)
	}
}

// ---------------------------------------------------------------------------
// SC3: Unknown host → clear error response
// ---------------------------------------------------------------------------

// TestSC3_UnknownHostReturnsError verifies that HTTP requests for an
// unregistered hostname receive a clear error response.
func TestSC3_UnknownHostReturnsError(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	time.Sleep(20 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", infra.httpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("SC3: dial HTTP proxy: %v", err)
	}
	defer conn.Close()

	req := "GET / HTTP/1.1\r\nHost: unknown.nonexistent\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("SC3: write request: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	resp, err := io.ReadAll(conn)
	if err != nil && err != io.EOF {
		t.Fatalf("SC3: read response: %v", err)
	}

	respStr := string(resp)
	if !strings.Contains(respStr, "502") && !strings.Contains(respStr, "Bad Gateway") &&
		!strings.Contains(respStr, "No tunnel") {
		t.Errorf("SC3: expected 502/error response for unknown host, got: %q", respStr)
	}
}

// TestSC3_UnknownSNIClosesConnection verifies that HTTPS connections for
// an unregistered SNI hostname result in a closed connection.
func TestSC3_UnknownSNIClosesConnection(t *testing.T) {
	infra := startHTTPInfra(t)
	defer infra.shutdown()

	time.Sleep(20 * time.Millisecond)

	tlsCfg := &tls.Config{
		ServerName:         "nobody.nonexistent",
		InsecureSkipVerify: true, //nolint:gosec // test only
	}

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", infra.httpsAddr, tlsCfg,
	)

	if err != nil {
		// Connection rejected / closed before TLS handshake — SC3 satisfied.
		return
	}

	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	if readErr == nil {
		t.Errorf("SC3 HTTPS: expected connection to be closed for unknown SNI, but read succeeded")
	}
}

// ---------------------------------------------------------------------------
// HTTP / TLS helper functions
// ---------------------------------------------------------------------------

// doHTTPRequest sends a plain HTTP GET to proxyAddr with the given Host header
// and returns the full raw response as a string.
func doHTTPRequest(t *testing.T, proxyAddr, host, path string) string {
	t.Helper()

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial HTTP proxy: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write HTTP request: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	raw, err := io.ReadAll(conn)
	if err != nil && err != io.EOF {
		t.Fatalf("read HTTP response: %v", err)
	}
	return string(raw)
}

// doTLSEchoThrough dials the HTTPS proxy with the given TLS config (which sets
// the SNI via ServerName), sends msg, half-closes, and returns the echoed string.
func doTLSEchoThrough(t *testing.T, proxyAddr string, clientTLS *tls.Config, msg string) string {
	t.Helper()

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp", proxyAddr, clientTLS,
	)
	if err != nil {
		t.Fatalf("TLS dial through proxy: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("TLS write: %v", err)
	}
	_ = conn.CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("TLS read echo: %v", err)
	}
	return string(buf)
}
