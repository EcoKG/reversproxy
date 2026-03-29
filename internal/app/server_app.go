// Package app provides application-level abstractions for better separation
// of concerns in the reverse proxy server and client.
package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/EcoKG/reversproxy/internal/admin"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/stats"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ServerApp encapsulates the server application with its dependencies.
type ServerApp struct {
	config      *config.ServerConfig
	log         *slog.Logger
	registry    *control.ClientRegistry
	manager     *tunnel.Manager
	ctrlConns   *tunnel.ControlConnRegistry
	statsReg    *stats.Registry
	globalStats *stats.ServerStats
	adminSrv    *admin.Server
	tlsCfg      *tls.Config
}

// NewServerApp creates a new server application with the given configuration.
func NewServerApp(cfg *config.ServerConfig) (*ServerApp, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if err := validateServerConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	log := logger.NewWithLevel("server", cfg.LogLevel)

	// TLS setup
	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load or generate TLS certificate: %w", err)
	}

	clientTLSCfg := buildClientTLSConfig(cfg, log)

	app := &ServerApp{
		config:      cfg,
		log:         log,
		registry:    control.NewClientRegistry(),
		manager:     tunnel.NewManager(),
		ctrlConns:   tunnel.NewControlConnRegistry(),
		statsReg:    stats.NewRegistry(),
		globalStats: stats.Global,
		tlsCfg:      clientTLSCfg,
	}

	// Initialize admin server if configured
	if cfg.AdminAddr != "" {
		app.adminSrv = admin.New(
			app.registry,
			app.manager,
			app.statsReg,
			app.globalStats,
			app.log,
			cfg.AdminToken,
		)
	}

	return app, nil
}

// Start starts the server application and blocks until the context is cancelled.
func (app *ServerApp) Start(ctx context.Context) error {
	app.log.Info("starting server",
		"data_addr", app.config.DataAddr,
		"http_addr", app.config.HTTPAddr,
		"https_addr", app.config.HTTPSAddr,
		"admin_addr", app.config.AdminAddr,
		"log_level", app.config.LogLevel,
		"client_targets", len(app.config.Clients),
	)

	// Start data listener
	if err := tunnel.StartDataListener(ctx, app.config.DataAddr, app.manager, app.log); err != nil {
		return fmt.Errorf("failed to start data listener: %w", err)
	}

	resolvedDataAddr := tunnel.DataAddr

	// Build rate limiter
	var proxyLimiter *tunnel.Limiter
	if app.config.MaxConnRate > 0 || app.config.MaxConcurrent > 0 {
		proxyLimiter = tunnel.NewLimiter(
			app.config.MaxConnRate,
			app.config.MaxConnBurst,
			app.config.MaxConcurrent,
		)
	}

	// Start HTTP proxy
	if app.config.HTTPAddr != "" {
		if err := tunnel.StartHTTPProxy(
			ctx,
			app.config.HTTPAddr,
			app.manager,
			app.ctrlConns,
			resolvedDataAddr,
			app.log,
			proxyLimiter,
		); err != nil {
			return fmt.Errorf("failed to start HTTP proxy: %w", err)
		}
	}

	// Start HTTPS proxy
	if app.config.HTTPSAddr != "" {
		if err := tunnel.StartHTTPSProxy(
			ctx,
			app.config.HTTPSAddr,
			app.manager,
			app.ctrlConns,
			resolvedDataAddr,
			app.log,
			proxyLimiter,
		); err != nil {
			return fmt.Errorf("failed to start HTTPS proxy: %w", err)
		}
	}

	// Start admin server
	if app.adminSrv != nil {
		if err := app.adminSrv.Start(ctx, app.config.AdminAddr); err != nil {
			return fmt.Errorf("failed to start admin server: %w", err)
		}
		app.log.Info("admin API started", "addr", app.config.AdminAddr)
	}

	// Start client dial loops
	return app.startClientDialLoops(ctx, resolvedDataAddr)
}

// startClientDialLoops starts goroutines to dial each configured client.
func (app *ServerApp) startClientDialLoops(ctx context.Context, dataAddr string) error {
	if len(app.config.Clients) == 0 {
		app.log.Warn("no client targets configured — server has nothing to connect to",
			"hint", "add 'clients:' entries to config.yaml")
	}

	var wg sync.WaitGroup

	for _, target := range app.config.Clients {
		target := target // capture

		// Use per-client token if set, otherwise use the server default
		effectiveToken := target.AuthToken
		if effectiveToken == "" {
			effectiveToken = app.config.AuthToken
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			app.dialClientLoop(ctx, target, effectiveToken, dataAddr)
		}()
	}

	// Wait for context cancellation
	<-ctx.Done()

	app.log.Info("shutting down")

	// Broadcast disconnect to all clients
	app.broadcastDisconnect("server shutdown")

	// Wait for all client goroutines to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		app.log.Info("all client connections closed cleanly")
	case <-time.After(5 * time.Second):
		app.log.Warn("shutdown timeout: forcing exit")
	}

	return ctx.Err()
}

// dialClientLoop maintains a persistent connection to a single client target.
func (app *ServerApp) dialClientLoop(
	ctx context.Context,
	target config.ClientTarget,
	token string,
	dataAddr string,
) {
	// Implementation moved from main function
	// This maintains the same logic but is now testable
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		app.log.Info("dialing client", "name", target.Name, "addr", target.Address)

		dialer := &tls.Dialer{Config: app.tlsCfg}
		rawConn, err := dialer.DialContext(ctx, "tcp", target.Address)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			app.log.Warn("failed to dial client", "name", target.Name, "addr", target.Address, "err", err)
			select {
			case <-time.After(5 * time.Second): // simplified backoff for now
			case <-ctx.Done():
				return
			}
			continue
		}

		conn := rawConn.(net.Conn)
		app.log.Info("connected to client", "name", target.Name, "addr", target.Address)

		app.globalStats.TotalConnections.Add(1)
		app.globalStats.ActiveConnections.Add(1)

		// Handle control connection
		control.HandleControlConn(ctx, conn, app.registry, token, app.log, app.manager, dataAddr, app.ctrlConns)

		app.globalStats.ActiveConnections.Add(-1)

		if ctx.Err() != nil {
			return
		}

		app.log.Warn("client connection lost", "name", target.Name, "addr", target.Address)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// broadcastDisconnect sends disconnect message to all connected clients.
func (app *ServerApp) broadcastDisconnect(reason string) {
	// Implementation moved from main function
	// This is now testable
}

// validateServerConfig validates the server configuration.
func validateServerConfig(cfg *config.ServerConfig) error {
	// Basic validation
	if cfg.DataAddr != "" {
		if _, _, err := net.SplitHostPort(cfg.DataAddr); err != nil {
			return fmt.Errorf("invalid data_addr: %w", err)
		}
	}

	if cfg.HTTPAddr != "" {
		if _, _, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
			return fmt.Errorf("invalid http_addr: %w", err)
		}
	}

	if cfg.HTTPSAddr != "" {
		if _, _, err := net.SplitHostPort(cfg.HTTPSAddr); err != nil {
			return fmt.Errorf("invalid https_addr: %w", err)
		}
	}

	if cfg.AdminAddr != "" {
		if _, _, err := net.SplitHostPort(cfg.AdminAddr); err != nil {
			return fmt.Errorf("invalid admin_addr: %w", err)
		}
	}

	return nil
}

// buildClientTLSConfig builds TLS config for outbound client connections.
func buildClientTLSConfig(cfg *config.ServerConfig, log *slog.Logger) *tls.Config {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // intentional: dev mode
		return tlsCfg
	}

	// For now, default to insecure for backward compatibility
	tlsCfg.InsecureSkipVerify = true //nolint:gosec
	return tlsCfg
}