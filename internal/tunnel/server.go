package tunnel

import (
	"bytes"
	"context"
	"encoding/gob"
	"io"
	"log/slog"
	"net"

	"github.com/google/uuid"
	"github.com/starlyn/reversproxy/internal/protocol"
)

// DataAddr is the address (host:port) on which the server listens for
// incoming data connections from clients. Clients dial this address after
// receiving an OpenConnection message.
var DataAddr string

// StartDataListener starts the server-side data connection listener on addr.
// When a client dials in, it sends a DataConnHello; the server looks up the
// matching pendingConn and fulfils it so the relay can proceed.
func StartDataListener(ctx context.Context, addr string, mgr *Manager, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	DataAddr = ln.Addr().String()
	log.Info("data listener started", "addr", DataAddr)

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
			go handleDataConn(conn, mgr, log)
		}
	}()

	return nil
}

// handleDataConn reads the DataConnHello from a client data connection and
// fulfils the pending external connection so the relay can start.
func handleDataConn(conn net.Conn, mgr *Manager, log *slog.Logger) {
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("data conn: failed to read hello", "err", err)
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
		go relayExternalConn(ctx, pending, connID, log)
	}
}

// relayExternalConn waits for the client's data connection to arrive and then
// relays data bidirectionally between the external user and the client.
func relayExternalConn(ctx context.Context, pending *pendingConn, connID string, log *slog.Logger) {
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
		PendingExtConn(pending).Close()
		return
	}

	extConn := PendingExtConn(pending)

	log.Info("relay started", "connID", connID)

	// Bidirectional relay: copy in both directions concurrently.
	done := make(chan struct{}, 2)

	go func() {
		_, err := io.Copy(dataConn, extConn)
		if err != nil && !isClosedErr(err) {
			log.Debug("relay ext→data done", "connID", connID, "err", err)
		}
		_ = dataConn.(*net.TCPConn).CloseWrite()
		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(extConn, dataConn)
		if err != nil && !isClosedErr(err) {
			log.Debug("relay data→ext done", "connID", connID, "err", err)
		}
		if tc, ok := extConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	extConn.Close()
	dataConn.Close()

	log.Info("relay finished", "connID", connID)
}

// isClosedErr returns true for "use of closed network connection" errors that
// occur naturally when we close a connection to unblock a goroutine.
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	return containsStr(err.Error(), "use of closed network connection") ||
		containsStr(err.Error(), "EOF")
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && searchStr(s, sub))
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
