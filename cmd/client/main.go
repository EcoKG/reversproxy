package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/gob"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

func main() {
	// ------------------------------------------------------------------ //
	// Flags — config file is loaded first; flags override.
	// ------------------------------------------------------------------ //
	configFile := flag.String("config",      "config.yaml",  "path to YAML config file (optional)")
	listenAddr := flag.String("listen",      "",             "listen address for server connections (overrides config)")
	token      := flag.String("token",       "",             "pre-shared auth token (overrides config)")
	name       := flag.String("name",        "",             "client label (overrides config)")
	insecure   := flag.Bool("insecure",      false,          "skip TLS certificate verification (overrides config)")
	localHost  := flag.String("local-host",  "127.0.0.1",    "local service hostname to tunnel")
	localPort  := flag.Int("local-port",     0,              "local service port to tunnel (0 = no tunnel)")
	pubPort    := flag.Int("pub-port",       0,              "requested public port on server (0 = any)")
	httpHost   := flag.String("http-host",   "",             "hostname to register for HTTP host-based routing")
	httpPort   := flag.Int("http-port",      0,              "local port for HTTP routing")
	httpsHost  := flag.String("https-host",  "",             "hostname to register for HTTPS SNI routing")
	httpsPort  := flag.Int("https-port",     0,              "local port for HTTPS routing")
	socksAddr     := flag.String("socks-addr",       "",             "local SOCKS5 listener address (overrides config; empty = use config default)")
	socksUser     := flag.String("socks-user",       "",             "SOCKS5 auth username (overrides config; empty = no auth)")
	socksPass     := flag.String("socks-pass",       "",             "SOCKS5 auth password (overrides config; empty = no auth)")
	httpProxyAddr := flag.String("http-proxy-addr",  "",             "local HTTP CONNECT proxy address (overrides config; empty = use config default)")
	logLevel   := flag.String("log-level",   "",             "log level: debug/info/warn/error (overrides config)")
	certFile   := flag.String("cert",        "",             "TLS certificate file path (overrides config)")
	keyFile    := flag.String("key",         "",             "TLS private key file path (overrides config)")
	flag.Parse()

	// ------------------------------------------------------------------ //
	// Load config file; then apply flag overrides.
	// ------------------------------------------------------------------ //
	cfg, err := config.LoadClientConfig(*configFile)
	if err != nil {
		tmpLog := logger.New("client")
		tmpLog.Error("failed to load config file", "path", *configFile, "err", err)
		return
	}

	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.ListenAddr = *listenAddr
		case "token":
			cfg.AuthToken = *token
		case "name":
			cfg.Name = *name
		case "insecure":
			cfg.Insecure = *insecure
		case "log-level":
			cfg.LogLevel = *logLevel
		case "cert":
			cfg.CertPath = *certFile
		case "key":
			cfg.KeyPath = *keyFile
		case "socks-addr":
			cfg.SOCKSAddr = *socksAddr
		case "socks-user":
			cfg.SOCKSUser = *socksUser
		case "socks-pass":
			cfg.SOCKSPass = *socksPass
		case "http-proxy-addr":
			cfg.HTTPProxyAddr = *httpProxyAddr
		}
	})

	log := logger.NewWithLevel("client", cfg.LogLevel)

	// ------------------------------------------------------------------ //
	// Build the tunnel configuration.
	// Config-file tunnels take effect first; flag-based tunnels are appended.
	// ------------------------------------------------------------------ //
	rcCfg := &reconnect.ClientConfig{}

	for _, t := range cfg.Tunnels {
		switch t.Type {
		case "tcp", "":
			rcCfg.AddTunnel(t.LocalHost, t.LocalPort, t.RequestedPort)
		case "http":
			rcCfg.AddHTTPTunnel(t.Hostname, t.LocalHost, t.LocalPort)
		case "https":
			rcCfg.AddHTTPSTunnel(t.Hostname, t.LocalHost, t.LocalPort)
		}
	}

	// Legacy flag-based tunnel configuration (backward-compatible).
	if *localPort > 0 {
		rcCfg.AddTunnel(*localHost, *localPort, *pubPort)
	}
	if *httpHost != "" && *httpPort > 0 {
		rcCfg.AddHTTPTunnel(*httpHost, *localHost, *httpPort)
	}
	if *httpsHost != "" && *httpsPort > 0 {
		rcCfg.AddHTTPSTunnel(*httpsHost, *localHost, *httpsPort)
	}

	// ------------------------------------------------------------------ //
	// TLS setup — client now listens; it needs a server certificate.
	// ------------------------------------------------------------------ //
	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		return
	}

	tlsCfg := control.NewServerTLSConfig(cert)
	if cfg.Insecure {
		// When insecure mode is enabled, also accept connections without verifying
		// the server's (dialer's) certificate. This is for dev/testing only.
		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		}
	}

	// ------------------------------------------------------------------ //
	// Start TLS listener — the client waits for server connections.
	// ------------------------------------------------------------------ //
	ln, err := tls.Listen("tcp", cfg.ListenAddr, tlsCfg)
	if err != nil {
		log.Error("failed to start TLS listener", "addr", cfg.ListenAddr, "err", err)
		return
	}
	defer ln.Close()

	log.Info("client listening for server connections", "addr", ln.Addr().String())
	fmt.Printf("Client listening on %s — waiting for proxy server to connect\n", ln.Addr().String())

	// ------------------------------------------------------------------ //
	// Signal handling
	// ------------------------------------------------------------------ //
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Close the listener when context is cancelled so Accept() returns.
	go func() {
		<-ctx.Done()
		log.Info("client shutting down")
		_ = ln.Close()
	}()

	// ------------------------------------------------------------------ //
	// Persistent SOCKS5 / HTTP CONNECT proxies
	//
	// These are started ONCE and survive server reconnections. The
	// sharedWriter's underlying connection is swapped on each reconnect.
	// ------------------------------------------------------------------ //
	sharedWriter := &clientConnWriter{}
	clientMux := tunnel.NewSOCKSMux()

	if cfg.SOCKSAddr != "" {
		if err := socks.StartClientSOCKSProxy(ctx, cfg.SOCKSAddr, sharedWriter, clientMux, log, cfg.SOCKSUser, cfg.SOCKSPass); err != nil {
			log.Error("failed to start client SOCKS5 proxy", "addr", cfg.SOCKSAddr, "err", err)
		} else {
			fmt.Printf("SOCKS5 proxy: socks5://127.0.0.1%s\n", socks.LastClientSOCKSAddr)
		}
	}

	if cfg.HTTPProxyAddr != "" {
		if err := socks.StartHTTPConnectProxy(ctx, cfg.HTTPProxyAddr, sharedWriter, clientMux, log); err != nil {
			log.Error("failed to start HTTP CONNECT proxy", "addr", cfg.HTTPProxyAddr, "err", err)
		} else {
			fmt.Printf("HTTP CONNECT proxy: http://%s (use HTTPS_PROXY)\n", socks.LastClientHTTPProxyAddr)
		}
	}

	// ------------------------------------------------------------------ //
	// Port forwards (built-in socat replacement)
	// ------------------------------------------------------------------ //
	for _, pf := range cfg.PortForwards {
		if err := socks.StartPortForward(ctx, pf.LocalPort, pf.RemoteHost, pf.RemotePort, pf.Bind, sharedWriter, clientMux, log); err != nil {
			log.Error("failed to start port forward", "localPort", pf.LocalPort, "err", err)
		} else {
			fmt.Printf("Port forward: localhost:%d → %s:%d\n", pf.LocalPort, pf.RemoteHost, pf.RemotePort)
		}
	}

	// ------------------------------------------------------------------ //
	// Accept loop — handle each incoming server connection
	// ------------------------------------------------------------------ //
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				log.Info("listener closed, stopping accept loop")
			default:
				log.Error("accept error", "err", err)
			}
			return
		}

		log.Info("server connected", "remote", conn.RemoteAddr())

		// Swap the writer to the new connection and clear stale mux channels.
		sharedWriter.SwapConn(conn)
		clientMux.CloseAll()

		// Handle this connection (blocks until lost).
		handleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, sharedWriter, clientMux, log)

		// Connection lost — clear writer so SOCKS/HTTP return 502 instead of writing to dead conn.
		sharedWriter.ClearConn()
		log.Warn("server connection lost, waiting for reconnect")
	}
}

// handleServerConn manages a single connection from the proxy server.
// It:
//  1. Performs the reversed registration handshake.
//  2. Re-registers all configured tunnels.
//  3. Runs the message loop until the connection is lost or ctx is cancelled.
//
// The SOCKS5/HTTP CONNECT proxies are started once in main() and share the
// swappable sharedWriter and clientMux passed here.
func handleServerConn(
	ctx context.Context,
	conn net.Conn,
	authToken, name string,
	cfg *reconnect.ClientConfig,
	sharedWriter *clientConnWriter,
	clientMux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	defer conn.Close()

	// ------------------------------------------------------------------ //
	// Registration handshake (reversed)
	// Server sends ClientRegister → client validates → client sends RegisterResp
	// ------------------------------------------------------------------ //

	// Give the handshake 10 seconds to complete.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Error("failed to set registration deadline", "err", err)
		return
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("failed to read registration message from server", "err", err)
		return
	}

	if env.Type != protocol.MsgClientRegister {
		log.Warn("unexpected message type during handshake", "type", env.Type)
		return
	}

	var msg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&msg); err != nil {
		log.Warn("failed to decode ClientRegister from server", "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "malformed ClientRegister payload",
		})
		return
	}

	if msg.AuthToken != authToken {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "invalid token",
		})
		log.Warn("registration rejected: invalid token from server", "remote", conn.RemoteAddr())
		return
	}

	// Send RegisterResp with the client's name in ServerID so the server knows
	// which client it has connected to.
	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: name, // client's name carried in ServerID field
	}); err != nil {
		log.Error("failed to send RegisterResp", "err", err)
		return
	}

	log.Info("registered with server", "remote", conn.RemoteAddr(), "client_name", name)

	// Remove the registration deadline.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Error("failed to clear deadline", "err", err)
		return
	}

	// ------------------------------------------------------------------ //
	// Re-register all tunnels
	// ------------------------------------------------------------------ //
	tunnelDataAddrs := make(map[string]string)
	var serverDataAddr string // first data addr learned from any tunnel registration

	for _, tc := range cfg.Tunnels {
		req := protocol.RequestTunnel{
			LocalHost:     tc.LocalHost,
			LocalPort:     tc.LocalPort,
			RequestedPort: tc.RequestedPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, req); err != nil {
			log.Warn("send RequestTunnel failed", "err", err)
			return
		}

		tenv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read TunnelResp failed", "err", err)
			return
		}
		if tenv.Type != protocol.MsgTunnelResp {
			log.Warn("expected TunnelResp", "got", tenv.Type)
			return
		}

		var tresp protocol.TunnelResp
		if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
			log.Warn("decode TunnelResp failed", "err", err)
			return
		}

		if tresp.Status != "ok" {
			log.Warn("tunnel request failed", "err", tresp.Error)
			return
		}

		tunnelDataAddrs[tresp.TunnelID] = tresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = tresp.ServerDataAddr
		}
		log.Info("tunnel established",
			"tunnelID", tresp.TunnelID,
			"publicPort", tresp.PublicPort,
			"serverDataAddr", tresp.ServerDataAddr,
		)
		fmt.Printf("Tunnel: 0.0.0.0:%d → %s:%d\n", tresp.PublicPort, tc.LocalHost, tc.LocalPort)
	}

	// Register HTTP tunnels.
	for _, hc := range cfg.HTTPTunnels {
		req := protocol.RequestHTTPTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPTunnel, req); err != nil {
			log.Warn("send RequestHTTPTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTP tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTP tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTP Tunnel: http://%s → %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	// Register HTTPS tunnels.
	for _, hc := range cfg.HTTPSTunnels {
		req := protocol.RequestHTTPSTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPSTunnel, req); err != nil {
			log.Warn("send RequestHTTPSTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp (HTTPS)", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTPS tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTPS tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTPS Tunnel: https://%s → %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	// ------------------------------------------------------------------ //
	// Graceful shutdown for this connection
	// ------------------------------------------------------------------ //
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	go func() {
		<-connCtx.Done()
		if ctx.Err() != nil {
			// Global shutdown — send disconnect
			log.Info("signal received, sending disconnect")
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	}()

	// ------------------------------------------------------------------ //
	// Message loop
	// ------------------------------------------------------------------ //
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		env, err := protocol.ReadMessage(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("connection to server lost", "err", err)
			return
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				log.Warn("failed to decode Ping", "err", err)
				continue
			}
			if err := sharedWriter.WriteMsg(protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn("send Pong failed", "err", err)
				return
			}
			log.Debug("pong sent", "seq", ping.Seq)

		case protocol.MsgDisconnect:
			var disc protocol.Disconnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&disc); err != nil {
				log.Warn("failed to decode Disconnect from server", "err", err)
			} else {
				log.Info("server requested disconnect", "reason", disc.Reason)
			}
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnectAck, protocol.DisconnectAck{})
			return

		case protocol.MsgOpenConnection:
			var openConn protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&openConn); err != nil {
				log.Warn("failed to decode OpenConnection", "err", err)
				continue
			}
			dataAddr, ok := tunnelDataAddrs[openConn.TunnelID]
			if !ok {
				log.Warn("received OpenConnection for unknown tunnelID",
					"tunnelID", openConn.TunnelID,
					"known_tunnels", len(tunnelDataAddrs),
				)
				continue
			}
			tunnel.HandleOpenConnection(openConn, dataAddr, log)

		// ---- Reversed SOCKS5 messages (Phase 4 reversed) ----
		// The server sends these back to us after we sent MsgSOCKSConnect.

		case protocol.MsgSOCKSReady:
			var ready protocol.SOCKSReady
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
				log.Warn("failed to decode SOCKSReady", "err", err)
				continue
			}
			if err := clientMux.DeliverReady(ready.ConnID, ready.Success, ready.Error); err != nil {
				log.Debug("SOCKSReady deliver failed", "connID", ready.ConnID, "err", err)
			}

		case protocol.MsgSOCKSData:
			var sd protocol.SOCKSData
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
				log.Warn("failed to decode SOCKSData", "err", err)
				continue
			}
			if err := clientMux.Deliver(sd.ConnID, sd.Payload); err != nil {
				log.Debug("SOCKSData deliver failed", "connID", sd.ConnID, "err", err)
			}

		case protocol.MsgSOCKSClose:
			var sc protocol.SOCKSClose
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSClose", "err", err)
				continue
			}
			clientMux.DeliverClose(sc.ConnID)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}

// clientConnWriter wraps a net.Conn with a mutex so that all writes from
// concurrent goroutines (SOCKS relay goroutines + message loop) are serialised.
// It implements socks.ControlWriter.
// The underlying connection can be swapped via SwapConn when the server reconnects.
type clientConnWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (w *clientConnWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn == nil {
		return fmt.Errorf("no active control connection")
	}
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// SwapConn replaces the underlying connection (called on server reconnect).
func (w *clientConnWriter) SwapConn(c net.Conn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = c
}

// ClearConn sets the connection to nil (called when connection is lost).
func (w *clientConnWriter) ClearConn() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = nil
}
