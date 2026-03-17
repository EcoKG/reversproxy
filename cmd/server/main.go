package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/EcoKG/reversproxy/internal/admin"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/stats"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

func main() {
	// ------------------------------------------------------------------ //
	// Flags — define all flags; config file values are applied first, then
	// flags override them if the flag was explicitly set.
	// ------------------------------------------------------------------ //
	configFile := flag.String("config",      "config.yaml", "path to YAML config file (optional)")
	dataAddr   := flag.String("data-addr",   "",            "TCP data connection listen address (overrides config)")
	httpAddr   := flag.String("http-addr",   "",            "HTTP host-based proxy listen address (overrides config)")
	httpsAddr  := flag.String("https-addr",  "",            "HTTPS SNI-routing proxy listen address (overrides config)")
	adminAddr  := flag.String("admin-addr",  "",            "Admin HTTP API listen address (overrides config)")
	token      := flag.String("token",       "",            "default pre-shared auth token (overrides config)")
	certFile   := flag.String("cert",        "",            "TLS certificate file path (overrides config)")
	keyFile    := flag.String("key",         "",            "TLS private key file path (overrides config)")
	logLevel   := flag.String("log-level",   "",            "log level: debug/info/warn/error (overrides config)")
	flag.Parse()

	// ------------------------------------------------------------------ //
	// Load config file first; command-line flags take precedence.
	// ------------------------------------------------------------------ //
	cfg, err := config.LoadServerConfig(*configFile)
	if err != nil {
		tmpLog := logger.New("server")
		tmpLog.Error("failed to load config file", "path", *configFile, "err", err)
		os.Exit(1)
	}

	// Apply flag overrides — only when the flag was explicitly provided.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "data-addr":
			cfg.DataAddr = *dataAddr
		case "http-addr":
			cfg.HTTPAddr = *httpAddr
		case "https-addr":
			cfg.HTTPSAddr = *httpsAddr
		case "admin-addr":
			cfg.AdminAddr = *adminAddr
		case "token":
			cfg.AuthToken = *token
		case "cert":
			cfg.CertPath = *certFile
		case "key":
			cfg.KeyPath = *keyFile
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	// ------------------------------------------------------------------ //
	// Logger
	// ------------------------------------------------------------------ //
	log := logger.NewWithLevel("server", cfg.LogLevel)

	log.Info("server configuration loaded",
		"data_addr", cfg.DataAddr,
		"http_addr", cfg.HTTPAddr,
		"https_addr", cfg.HTTPSAddr,
		"admin_addr", cfg.AdminAddr,
		"log_level", cfg.LogLevel,
		"client_targets", len(cfg.Clients),
	)

	// ------------------------------------------------------------------ //
	// TLS setup — used by the server when dialing clients
	// ------------------------------------------------------------------ //
	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		os.Exit(1)
	}
	_ = cert // cert is available if needed; for dialing clients we use InsecureSkipVerify by default

	// ------------------------------------------------------------------ //
	// Registry, tunnel manager, stats, and root context
	// ------------------------------------------------------------------ //
	reg       := control.NewClientRegistry()
	mgr       := tunnel.NewManager()
	ctrlConns := tunnel.NewControlConnRegistry()
	statsReg  := stats.NewRegistry()
	globalStats := stats.Global

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the data connection listener.
	if err := tunnel.StartDataListener(ctx, cfg.DataAddr, mgr, log); err != nil {
		log.Error("failed to start data listener", "addr", cfg.DataAddr, "err", err)
		os.Exit(1)
	}

	// dataAddr may have been :0 (OS-assigned); use the actual bound address.
	resolvedDataAddr := tunnel.DataAddr

	// Start the HTTP host-based proxy.
	if cfg.HTTPAddr != "" {
		if err := tunnel.StartHTTPProxy(ctx, cfg.HTTPAddr, mgr, ctrlConns, resolvedDataAddr, log); err != nil {
			log.Error("failed to start HTTP proxy", "addr", cfg.HTTPAddr, "err", err)
			os.Exit(1)
		}
	}

	// Start the HTTPS SNI-routing proxy.
	if cfg.HTTPSAddr != "" {
		if err := tunnel.StartHTTPSProxy(ctx, cfg.HTTPSAddr, mgr, ctrlConns, resolvedDataAddr, log); err != nil {
			log.Error("failed to start HTTPS proxy", "addr", cfg.HTTPSAddr, "err", err)
			os.Exit(1)
		}
	}

	// Start the admin API server.
	if cfg.AdminAddr != "" {
		adminSrv := admin.New(reg, mgr, statsReg, globalStats, log)
		if err := adminSrv.Start(ctx, cfg.AdminAddr); err != nil {
			log.Error("failed to start admin server", "addr", cfg.AdminAddr, "err", err)
			os.Exit(1)
		}
		log.Info("admin API started", "addr", cfg.AdminAddr)
	}

	// ------------------------------------------------------------------ //
	// Graceful shutdown signal handler
	// ------------------------------------------------------------------ //
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Info("shutting down", "signal", sig.String())

		// Cancel root context — propagates to all client goroutines.
		cancel()

		// Broadcast Disconnect to every connected client.
		for _, c := range reg.List() {
			_ = protocol.WriteMessage(c.Conn, protocol.MsgDisconnect, protocol.Disconnect{
				Reason: "server shutdown",
			})
		}
	}()

	// ------------------------------------------------------------------ //
	// Dial loop — server connects to each configured client
	//
	// For each client target the server maintains a persistent goroutine that
	// dials the client's listen address. If the connection is lost, it retries
	// with exponential backoff. Each connection is handed off to
	// HandleControlConn which sends the registration handshake, then manages
	// the tunnel session for that client.
	// ------------------------------------------------------------------ //

	if len(cfg.Clients) == 0 {
		log.Warn("no client targets configured — server has nothing to connect to",
			"hint", "add 'clients:' entries to config.yaml")
	}

	var wg sync.WaitGroup

	for _, target := range cfg.Clients {
		target := target // capture

		// Use per-client token if set, otherwise use the server default.
		effectiveToken := target.AuthToken
		if effectiveToken == "" {
			effectiveToken = cfg.AuthToken
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			dialClientLoop(ctx, target, effectiveToken, reg, mgr, resolvedDataAddr, ctrlConns, log, globalStats)
		}()
	}

	// Wait for all client goroutines to finish (they exit when ctx is cancelled).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info("all client connections closed cleanly")
	case <-time.After(5 * time.Second):
		log.Warn("shutdown timeout: forcing exit")
	}

	os.Exit(0)
}

// dialClientLoop maintains a persistent connection to a single client target.
// It retries indefinitely with exponential backoff until ctx is cancelled.
func dialClientLoop(
	ctx context.Context,
	target config.ClientTarget,
	token string,
	reg *control.ClientRegistry,
	mgr *tunnel.Manager,
	dataAddr string,
	ctrlConns *tunnel.ControlConnRegistry,
	log *slog.Logger,
	globalStats *stats.ServerStats,
) {
	// Server dials clients with InsecureSkipVerify by default (self-signed certs).
	// For production, provide a trusted CA in the TLS config.
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional: clients use self-signed certs
		MinVersion:         tls.VersionTLS13,
	}

	backoff := reconnect.NewBackoff()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Info("dialing client", "name", target.Name, "addr", target.Address)

		dialer := &tls.Dialer{Config: tlsCfg}
		rawConn, err := dialer.DialContext(ctx, "tcp", target.Address)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			delay := backoff.Next()
			log.Warn("failed to dial client, retrying",
				"name", target.Name,
				"addr", target.Address,
				"err", err,
				"backoff", delay.String(),
			)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			continue
		}

		conn := rawConn.(net.Conn)
		log.Info("connected to client", "name", target.Name, "addr", target.Address)

		globalStats.TotalConnections.Add(1)
		globalStats.ActiveConnections.Add(1)

		// HandleControlConn blocks until the connection is closed.
		control.HandleControlConn(ctx, conn, reg, token, log, mgr, dataAddr, ctrlConns)

		globalStats.ActiveConnections.Add(-1)

		if ctx.Err() != nil {
			return
		}

		delay := backoff.Next()
		log.Warn("client connection lost, retrying",
			"name", target.Name,
			"addr", target.Address,
			"backoff", delay.String(),
		)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}

		// Reset backoff when we successfully reconnect (on next successful HandleControlConn).
		backoff.Reset()
	}
}
