package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/EcoKG/reversproxy/internal/client"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
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
	sharedWriter := &client.ConnWriter{}
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
		client.HandleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, sharedWriter, clientMux, log)

		// Connection lost — clear writer so SOCKS/HTTP return 502 instead of writing to dead conn.
		sharedWriter.ClearConn()
		log.Warn("server connection lost, waiting for reconnect")
	}
}
