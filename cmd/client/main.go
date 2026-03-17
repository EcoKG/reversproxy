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
	configFile := flag.String("config",      "config.yaml",     "path to YAML config file (optional)")
	server     := flag.String("server",      "",                "server address (overrides config)")
	token      := flag.String("token",       "",                "pre-shared auth token (overrides config)")
	name       := flag.String("name",        "",                "client label (overrides config)")
	insecure   := flag.Bool("insecure",      false,             "skip TLS certificate verification (overrides config)")
	localHost  := flag.String("local-host",  "127.0.0.1",       "local service hostname to tunnel")
	localPort  := flag.Int("local-port",     0,                 "local service port to tunnel (0 = no tunnel)")
	pubPort    := flag.Int("pub-port",       0,                 "requested public port on server (0 = any)")
	httpHost   := flag.String("http-host",   "",                "hostname to register for HTTP host-based routing")
	httpPort   := flag.Int("http-port",      0,                 "local port for HTTP routing")
	httpsHost  := flag.String("https-host",  "",                "hostname to register for HTTPS SNI routing")
	httpsPort  := flag.Int("https-port",     0,                 "local port for HTTPS routing")
	logLevel   := flag.String("log-level",   "",                "log level: debug/info/warn/error (overrides config)")
	flag.Parse()

	// ------------------------------------------------------------------ //
	// Load config file; then apply flag overrides.
	// ------------------------------------------------------------------ //
	cfg, err := config.LoadClientConfig(*configFile)
	if err != nil {
		// Fallback logger for startup errors.
		tmpLog := logger.New("client")
		tmpLog.Error("failed to load config file", "path", *configFile, "err", err)
		return
	}

	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "server":
			cfg.ServerAddr = *server
		case "token":
			cfg.AuthToken = *token
		case "name":
			cfg.Name = *name
		case "insecure":
			cfg.Insecure = *insecure
		case "log-level":
			cfg.LogLevel = *logLevel
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

	tlsCfg := control.NewClientTLSConfig(cfg.Insecure)

	// ------------------------------------------------------------------ //
	// Signal handling
	// ------------------------------------------------------------------ //
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	backoff := reconnect.NewBackoff()

	serverAddr := cfg.ServerAddr

	// ------------------------------------------------------------------ //
	// Reconnect loop
	// ------------------------------------------------------------------ //
	for {
		select {
		case <-ctx.Done():
			log.Info("client shutting down")
			return
		default:
		}

		log.Info("connecting to server", "server", serverAddr)

		err := connect(ctx, tlsCfg, serverAddr, cfg.AuthToken, cfg.Name, rcCfg, log)
		if err != nil {
			if ctx.Err() != nil {
				log.Info("client shutting down")
				return
			}

			delay := backoff.Next()
			log.Warn("connection failed, will retry",
				"err", err,
				"backoff", delay.String(),
			)

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				log.Info("client shutting down during backoff")
				return
			}
			continue
		}

		log.Info("disconnected cleanly, not reconnecting")
		return
	}
}

// connect dials the server, performs the registration + tunnel setup, then
// runs the message loop until the connection is lost or the context is
// cancelled. It returns nil on a clean (graceful) disconnect and a non-nil
// error on any unexpected failure so the caller knows whether to retry.
func connect(
	ctx context.Context,
	tlsCfg *tls.Config,
	server, token, name string,
	cfg *reconnect.ClientConfig,
	log *slog.Logger,
) error {
	// ------------------------------------------------------------------ //
	// Dial
	// ------------------------------------------------------------------ //
	dialer := &tls.Dialer{Config: tlsCfg}
	rawConn, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn := rawConn.(net.Conn)
	defer conn.Close()

	// ------------------------------------------------------------------ //
	// Registration handshake
	// ------------------------------------------------------------------ //
	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: token,
		Name:      name,
		Version:   "0.1.0",
	}); err != nil {
		return fmt.Errorf("send ClientRegister: %w", err)
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("read RegisterResp: %w", err)
	}

	var resp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&resp); err != nil {
		return fmt.Errorf("decode RegisterResp: %w", err)
	}

	if resp.Status != "ok" {
		return fmt.Errorf("registration rejected: %s", resp.Error)
	}

	log.Info("registered", "client_id", resp.AssignedClientID)

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
			return fmt.Errorf("send RequestTunnel: %w", err)
		}

		tenv, err := protocol.ReadMessage(conn)
		if err != nil {
			return fmt.Errorf("read TunnelResp: %w", err)
		}
		if tenv.Type != protocol.MsgTunnelResp {
			return fmt.Errorf("expected TunnelResp, got %v", tenv.Type)
		}

		var tresp protocol.TunnelResp
		if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
			return fmt.Errorf("decode TunnelResp: %w", err)
		}

		if tresp.Status != "ok" {
			return fmt.Errorf("tunnel request failed: %s", tresp.Error)
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
			return fmt.Errorf("send RequestHTTPTunnel: %w", err)
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			return fmt.Errorf("read HTTPTunnelResp: %w", err)
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			return fmt.Errorf("expected MsgHTTPTunnelResp, got %v", henv.Type)
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			return fmt.Errorf("decode HTTPTunnelResp: %w", err)
		}

		if hresp.Status != "ok" {
			return fmt.Errorf("HTTP tunnel request failed: %s", hresp.Error)
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
			return fmt.Errorf("send RequestHTTPSTunnel: %w", err)
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			return fmt.Errorf("read HTTPTunnelResp (HTTPS): %w", err)
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			return fmt.Errorf("expected MsgHTTPTunnelResp, got %v", henv.Type)
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			return fmt.Errorf("decode HTTPTunnelResp (HTTPS): %w", err)
		}

		if hresp.Status != "ok" {
			return fmt.Errorf("HTTPS tunnel request failed: %s", hresp.Error)
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
			return nil
		default:
		}

		env, err := protocol.ReadMessage(conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("connection lost: %w", err)
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
					return nil
				}
				return fmt.Errorf("send Pong: %w", err)
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
			return nil

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
