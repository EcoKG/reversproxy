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

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
)

func main() {
	// ------------------------------------------------------------------ //
	// Flags
	// ------------------------------------------------------------------ //
	addr     := flag.String("addr",  ":8443",      "TLS listen address")
	token    := flag.String("token", "changeme",   "pre-shared auth token")
	certFile := flag.String("cert",  "server.crt", "TLS certificate file path")
	keyFile  := flag.String("key",   "server.key", "TLS private key file path")
	flag.Parse()

	// ------------------------------------------------------------------ //
	// Logger
	// ------------------------------------------------------------------ //
	log := logger.New("server")

	// ------------------------------------------------------------------ //
	// TLS setup
	// ------------------------------------------------------------------ //
	cert, err := control.LoadOrGenerateCert(*certFile, *keyFile)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		os.Exit(1)
	}

	tlsCfg := control.NewServerTLSConfig(cert)

	ln, err := tls.Listen("tcp", *addr, tlsCfg)
	if err != nil {
		log.Error("failed to start TLS listener", "addr", *addr, "err", err)
		os.Exit(1)
	}

	log.Info("server listening", "addr", *addr)

	// ------------------------------------------------------------------ //
	// Registry and root context
	// ------------------------------------------------------------------ //
	reg := control.NewClientRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			control.HandleControlConn(ctx, c, reg, *token, log)
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
