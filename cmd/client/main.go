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
	"syscall"
	"time"

	"github.com/starlyn/reversproxy/internal/config"
	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/reconnect"
	"github.com/starlyn/reversproxy/internal/tunnel"
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

		// Handle each server connection in its own goroutine.
		go handleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, log)
	}
}

// handleServerConn manages a single connection from the proxy server.
// It:
//  1. Performs the reversed registration handshake (reads ClientRegister from
//     server, validates the token, sends RegisterResp with the client's name).
//  2. Re-registers all configured tunnels.
//  3. Runs the message loop until the connection is lost or ctx is cancelled.
func handleServerConn(
	ctx context.Context,
	conn net.Conn,
	authToken, name string,
	cfg *reconnect.ClientConfig,
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
		log.Info("HTTPS tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTPS Tunnel: https://%s → %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	// ------------------------------------------------------------------ //
	// Graceful shutdown goroutine
	// ------------------------------------------------------------------ //
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()

		log.Info("signal received, sending disconnect")
		_ = protocol.WriteMessage(conn, protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})

		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = protocol.ReadMessage(conn)
		conn.Close()
	}()
	defer func() { <-shutdownDone }()

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
			if err := protocol.WriteMessage(conn, protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
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
			_ = protocol.WriteMessage(conn, protocol.MsgDisconnectAck, protocol.DisconnectAck{})
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

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}
