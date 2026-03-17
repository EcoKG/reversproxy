package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/starlyn/reversproxy/internal/admin"
	"github.com/starlyn/reversproxy/internal/config"
	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/stats"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

func main() {
	// ------------------------------------------------------------------ //
	// Flags — define all flags; config file values are applied first, then
	// flags override them if the flag was explicitly set.
	// ------------------------------------------------------------------ //
	configFile := flag.String("config", "config.yaml", "path to YAML config file (optional)")
	addr       := flag.String("addr",       "",           "TLS control listen address (overrides config)")
	dataAddr   := flag.String("data-addr",  "",           "TCP data connection listen address (overrides config)")
	httpAddr   := flag.String("http-addr",  "",           "HTTP host-based proxy listen address (overrides config)")
	httpsAddr  := flag.String("https-addr", "",           "HTTPS SNI-routing proxy listen address (overrides config)")
	adminAddr  := flag.String("admin-addr", "",           "Admin HTTP API listen address (overrides config)")
	token      := flag.String("token",      "",           "pre-shared auth token (overrides config)")
	certFile   := flag.String("cert",       "",           "TLS certificate file path (overrides config)")
	keyFile    := flag.String("key",        "",           "TLS private key file path (overrides config)")
	logLevel   := flag.String("log-level",  "",           "log level: debug/info/warn/error (overrides config)")
	flag.Parse()

	// ------------------------------------------------------------------ //
	// Load config file first; command-line flags take precedence.
	// ------------------------------------------------------------------ //
	cfg, err := config.LoadServerConfig(*configFile)
	if err != nil {
		// Use a temporary logger since we haven't configured the real one yet.
		tmpLog := logger.New("server")
		tmpLog.Error("failed to load config file", "path", *configFile, "err", err)
		os.Exit(1)
	}

	// Apply flag overrides — only when the flag was explicitly provided.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "addr":
			cfg.Addr = *addr
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
		"addr", cfg.Addr,
		"data_addr", cfg.DataAddr,
		"http_addr", cfg.HTTPAddr,
		"https_addr", cfg.HTTPSAddr,
		"admin_addr", cfg.AdminAddr,
		"log_level", cfg.LogLevel,
	)

	// ------------------------------------------------------------------ //
	// TLS setup
	// ------------------------------------------------------------------ //
	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		os.Exit(1)
	}

	tlsCfg := control.NewServerTLSConfig(cert)

	ln, err := tls.Listen("tcp", cfg.Addr, tlsCfg)
	if err != nil {
		log.Error("failed to start TLS listener", "addr", cfg.Addr, "err", err)
		os.Exit(1)
	}

	log.Info("control server listening", "addr", cfg.Addr)

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

		// Close the listener so Accept() returns immediately.
		_ = ln.Close()

		// Broadcast Disconnect to every connected client.
		for _, c := range reg.List() {
			_ = protocol.WriteMessage(c.Conn, protocol.MsgDisconnect, protocol.Disconnect{
				Reason: "server shutdown",
			})
		}
	}()

	// ------------------------------------------------------------------ //
	// Accept loop
	// ------------------------------------------------------------------ //
	var wg sync.WaitGroup

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener was closed during shutdown — exit cleanly.
			select {
			case <-ctx.Done():
				log.Info("listener closed, stopping accept loop")
			default:
				log.Error("accept error", "err", err)
			}
			break
		}

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			globalStats.TotalConnections.Add(1)
			globalStats.ActiveConnections.Add(1)
			defer globalStats.ActiveConnections.Add(-1)
			control.HandleControlConn(ctx, c, reg, cfg.AuthToken, log, mgr, resolvedDataAddr, ctrlConns)
		}(conn)
	}

	// Wait for all handlers to finish, with a 3-second hard timeout.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info("all connections closed cleanly")
	case <-time.After(3 * time.Second):
		log.Warn("shutdown timeout: forcing exit")
	}

	os.Exit(0)
}
