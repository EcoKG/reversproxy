package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/EcoKG/reversproxy/internal/protocol"
)

// LastHTTPAddr is set by StartHTTPProxy to the actual bound address (useful
// when addr is ":0" and the OS picks a port).
var LastHTTPAddr string

// StartHTTPProxy starts an HTTP reverse-proxy listener on addr.
//
// When a request arrives it:
//  1. Reads the HTTP request (to extract the Host header).
//  2. Looks up the matching HTTP tunnel in mgr.
//  3. Sends an OpenConnection message to the registered client via its control conn.
//  4. Waits for the client's data connection.
//  5. Replays the raw request bytes into the data connection and relays bidirectionally.
//
// This design keeps TLS termination and HTTP parsing at the client side for
// HTTPS tunnels; plain-HTTP tunnels are fully relayed at the TCP level.
func StartHTTPProxy(ctx context.Context, addr string, mgr *Manager, ctrlConns *ControlConnRegistry, dataAddr string, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http proxy: listen %s: %w", addr, err)
	}

	LastHTTPAddr = ln.Addr().String()
	log.Info("HTTP proxy listener started", "addr", LastHTTPAddr)

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
					log.Error("HTTP proxy accept error", "err", err)
				}
				return
			}
			go handleHTTPConn(ctx, conn, mgr, ctrlConns, dataAddr, log)
		}
	}()

	return nil
}

// handleHTTPConn handles a single inbound HTTP connection.
// It peeks the Host header, routes to the matching client, and relays.
func handleHTTPConn(
	ctx context.Context,
	extConn net.Conn,
	mgr *Manager,
	ctrlConns *ControlConnRegistry,
	dataAddr string,
	log *slog.Logger,
) {
	defer func() {
		// Only close if we haven't handed off to the relay goroutine.
		// The relay goroutine is responsible for closing after hand-off.
	}()

	// Set a deadline for reading the initial HTTP request.
	_ = extConn.SetDeadline(time.Now().Add(10 * time.Second))

	// Use a bufio reader so we can peek without consuming bytes.
	br := bufio.NewReader(extConn)

	req, err := http.ReadRequest(br)
	if err != nil {
		log.Warn("HTTP proxy: failed to read request", "err", err, "remote", extConn.RemoteAddr())
		writeHTTPError(extConn, http.StatusBadRequest, "Bad Request")
		extConn.Close()
		return
	}

	// Extract hostname (strip port if present).
	host := req.Host
	if h, _, err2 := net.SplitHostPort(host); err2 == nil {
		host = h
	}

	if host == "" {
		writeHTTPError(extConn, http.StatusBadRequest, "Missing Host header")
		extConn.Close()
		return
	}

	// Clear deadline before potentially blocking.
	_ = extConn.SetDeadline(time.Time{})

	entry, ok := mgr.GetHTTPTunnel(host)
	if !ok {
		log.Warn("HTTP proxy: no tunnel for host", "host", host)
		writeHTTPError(extConn, http.StatusBadGateway, fmt.Sprintf("No tunnel registered for host %q", host))
		extConn.Close()
		return
	}

	// Look up the client's control connection.
	clientConn, ok := ctrlConns.Get(entry.ClientID)
	if !ok {
		log.Warn("HTTP proxy: client not connected", "host", host, "clientID", entry.ClientID)
		writeHTTPError(extConn, http.StatusBadGateway, "Client tunnel not available")
		extConn.Close()
		return
	}

	connID := uuid.New().String()
	log.Info("HTTP proxy: routing request",
		"host", host,
		"connID", connID,
		"clientID", entry.ClientID,
		"localAddr", fmt.Sprintf("%s:%d", entry.LocalHost, entry.LocalPort),
	)

	// Rebuild the raw HTTP request bytes from the parsed request so we can
	// replay them into the data connection. We use a pipe: write the request
	// back to a net.Conn-compatible buffer.
	rawReqBuf := &peekBuffer{}
	_ = req.Write(rawReqBuf)

	// Register the pending connection (external conn) before sending OpenConnection.
	pending := mgr.RegisterPending(connID, extConn)

	openMsg := protocol.OpenConnection{
		TunnelID:  entry.ID,
		ConnID:    connID,
		LocalHost: entry.LocalHost,
		LocalPort: entry.LocalPort,
	}
	if err := protocol.WriteMessage(clientConn, protocol.MsgOpenConnection, openMsg); err != nil {
		log.Warn("HTTP proxy: failed to send OpenConnection", "connID", connID, "err", err)
		extConn.Close()
		return
	}

	// Relay in a goroutine; replay the raw HTTP request bytes first.
	go relayHTTPConn(ctx, pending, connID, rawReqBuf.Bytes(), log)
}

// relayHTTPConn waits for the client's data connection, replays the raw HTTP
// request bytes, then relays bidirectionally.
func relayHTTPConn(ctx context.Context, pending *pendingConn, connID string, rawReq []byte, log *slog.Logger) {
	waitDone := make(chan net.Conn, 1)
	go func() {
		waitDone <- WaitReady(pending)
	}()

	var dataConn net.Conn
	select {
	case dataConn = <-waitDone:
	case <-time.After(15 * time.Second):
		log.Warn("HTTP proxy: timeout waiting for data conn", "connID", connID)
		PendingExtConn(pending).Close()
		return
	case <-ctx.Done():
		PendingExtConn(pending).Close()
		return
	}

	extConn := PendingExtConn(pending)

	// Replay the parsed HTTP request bytes into the data connection so the
	// client's local service receives a complete HTTP request.
	if len(rawReq) > 0 {
		if _, err := dataConn.Write(rawReq); err != nil {
			log.Warn("HTTP proxy: failed to write raw request to data conn", "connID", connID, "err", err)
			extConn.Close()
			dataConn.Close()
			return
		}
	}

	log.Info("HTTP proxy: relay started", "connID", connID)

	done := make(chan struct{}, 2)

	go func() {
		copyAndClose(dataConn, extConn)
		done <- struct{}{}
	}()

	go func() {
		copyAndClose(extConn, dataConn)
		done <- struct{}{}
	}()

	<-done
	<-done

	extConn.Close()
	dataConn.Close()

	log.Info("HTTP proxy: relay finished", "connID", connID)
}

// writeHTTPError writes a minimal HTTP error response to conn.
func writeHTTPError(conn net.Conn, code int, msg string) {
	body := fmt.Sprintf("%d %s\n", code, msg)
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
	_, _ = conn.Write([]byte(resp))
}

// peekBuffer is a simple byte buffer that implements io.Writer for capturing
// the re-serialised HTTP request.
type peekBuffer struct {
	data []byte
}

func (p *peekBuffer) Write(b []byte) (int, error) {
	p.data = append(p.data, b...)
	return len(b), nil
}

func (p *peekBuffer) Bytes() []byte { return p.data }
