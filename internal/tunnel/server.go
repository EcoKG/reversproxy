package tunnel

import (
	"bytes"
	"context"
	"encoding/gob"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/protocol"
)

// StartDataListener starts the server-side data connection listener on addr.
// When a client dials in, it sends a DataConnHello; the server looks up the
// matching pendingConn and fulfils it so the relay can proceed.
// Returns the actual bound address (useful when addr uses port :0) and any error.
func StartDataListener(ctx context.Context, addr string, mgr *Manager, log *slog.Logger) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}

	boundAddr := ln.Addr().String()
	log.Info("data listener started", "addr", boundAddr)

	go func() {
		defer ln.Close()

		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()

		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
				default:
					log.Error("data listener accept error", "err", err)
				}
				return
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetKeepAlive(true)
				_ = tc.SetKeepAlivePeriod(15 * time.Second)
			}
			go handleDataConn(conn, mgr, log)
		}
	}()

	return boundAddr, nil
}

// handleDataConn reads the DataConnHello from a client data connection and
// fulfils the pending external connection so the relay can start.
func handleDataConn(conn net.Conn, mgr *Manager, log *slog.Logger) {
	// Apply a deadline for reading the handshake to avoid goroutine leaks.
	if err := conn.SetDeadline(time.Now().Add(config.HandshakeTimeout)); err != nil {
		log.Warn("data conn: failed to set deadline", "err", err)
		conn.Close()
		return
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("data conn: failed to read hello", "err", err)
		conn.Close()
		return
	}

	// Clear deadline for subsequent relay use.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Warn("data conn: failed to clear deadline", "err", err)
		conn.Close()
		return
	}

	if env.Type != protocol.MsgDataConnHello {
		log.Warn("data conn: unexpected message type", "type", env.Type)
		conn.Close()
		return
	}

	var hello protocol.DataConnHello
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&hello); err != nil {
		log.Warn("data conn: failed to decode hello", "err", err)
		conn.Close()
		return
	}

	if err := mgr.FulfillPending(hello.ConnID, conn); err != nil {
		log.Warn("data conn: fulfill failed", "connID", hello.ConnID, "err", err)
		conn.Close()
		return
	}

	log.Info("data conn: fulfilled", "connID", hello.ConnID)
}

// StartPublicListener opens a TCP listener on the requested public port and
// begins accepting external connections for the given tunnel. For each
// external connection it signals the client via the control connection and
// then relays data once the client's data connection arrives.
//
// It blocks until ctx is cancelled or the listener is closed.
func StartPublicListener(
	ctx context.Context,
	entry *TunnelEntry,
	clientConn net.Conn,
	mgr *Manager,
	log *slog.Logger,
) {
	log = log.With("tunnelID", entry.ID, "publicPort", entry.PublicPort)

	go func() {
		<-ctx.Done()
		_ = entry.listener.Close()
	}()

	for {
		extConn, err := entry.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				log.Info("public listener closed (context cancelled)")
			default:
				log.Error("public listener accept error", "err", err)
			}
			return
		}
		if tc, ok := extConn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(15 * time.Second)
		}

		connID := uuid.New().String()
		log.Info("external connection received", "connID", connID, "remoteAddr", extConn.RemoteAddr())

		// Register the pending external conn before notifying the client.
		pending := mgr.RegisterPending(connID, extConn)

		// Notify the client to open a data connection.
		openMsg := protocol.OpenConnection{
			TunnelID:  entry.ID,
			ConnID:    connID,
			LocalHost: entry.LocalHost,
			LocalPort: entry.LocalPort,
		}
		if err := protocol.WriteMessage(clientConn, protocol.MsgOpenConnection, openMsg); err != nil {
			log.Warn("failed to send OpenConnection to client", "connID", connID, "err", err)
			extConn.Close()
			continue
		}

		// Relay in a separate goroutine so we can continue accepting.
		go relayExternalConn(ctx, pending, connID, mgr, log)
	}
}

// relayExternalConn waits for the client's data connection to arrive and then
// relays data bidirectionally between the external user and the client.
// If the context is cancelled before the data connection arrives, the pending
// entry is removed from the manager to avoid a map leak.
func relayExternalConn(ctx context.Context, pending *pendingConn, connID string, mgr *Manager, log *slog.Logger) {
	// Wait for the client to dial back with the matching data connection.
	// Use a select with context so we don't block forever if the client dies.
	waitDone := make(chan net.Conn, 1)
	go func() {
		waitDone <- WaitReady(pending)
	}()

	var dataConn net.Conn
	select {
	case dataConn = <-waitDone:
	case <-ctx.Done():
		log.Warn("context cancelled while waiting for data conn", "connID", connID)
		mgr.CancelPending(connID)
		PendingExtConn(pending).Close()
		return
	}

	extConn := PendingExtConn(pending)

	log.Info("relay started", "connID", connID)

	RelayBiDirectional(ctx, extConn, dataConn)

	log.Info("relay finished", "connID", connID)
}
