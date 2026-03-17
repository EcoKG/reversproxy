// Package socks — CLIENT-side HTTP CONNECT proxy listener.
//
// This provides an HTTP CONNECT proxy on the client (closed-network) side that
// reuses the same tunnel mux as the SOCKS5 proxy.  Tools that speak HTTP CONNECT
// (such as Claude Code via HTTPS_PROXY=http://127.0.0.1:8080) can use this as
// a drop-in alternative to the SOCKS5 listener.
//
// Data flow:
//
//	HTTP CONNECT user → Client:8080 → MsgSOCKSConnect/Data/Close → Server → Internet
//
// The HTTP CONNECT frontend is just a different handshake on top of the same
// tunnel multiplexer used by the SOCKS5 proxy.  No new message types are
// needed.
package socks

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// LastClientHTTPProxyAddr is the actual bound address after
// StartHTTPConnectProxy.  Useful when addr uses ":0".
var LastClientHTTPProxyAddr string

// StartHTTPConnectProxy starts a local HTTP CONNECT proxy listener on addr.
//
//   - cw  — thread-safe writer for the control connection to the server.
//   - mux — the SOCKSMux that the client message loop uses to dispatch inbound
//     MsgSOCKSData / MsgSOCKSClose / MsgSOCKSReady frames (shared with the
//     SOCKS5 proxy if both are running).
func StartHTTPConnectProxy(
	ctx context.Context,
	addr string,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http connect proxy: listen %s: %w", addr, err)
	}

	LastClientHTTPProxyAddr = ln.Addr().String()
	log.Info("client HTTP CONNECT proxy started", "addr", LastClientHTTPProxyAddr)

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
					log.Error("http connect proxy: accept error", "err", err)
				}
				return
			}
			go handleHTTPConnectConn(ctx, conn, cw, mux, log)
		}
	}()

	return nil
}

// handleHTTPConnectConn handles one inbound HTTP CONNECT connection.
func handleHTTPConnectConn(
	ctx context.Context,
	conn net.Conn,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReader(conn)

	// ------------------------------------------------------------------ //
	// Phase 1 — Read request line: METHOD target HTTP/1.x
	// ------------------------------------------------------------------ //

	requestLine, err := br.ReadString('\n')
	if err != nil {
		log.Debug("http connect proxy: failed to read request line", "err", err)
		return
	}
	requestLine = strings.TrimRight(requestLine, "\r\n")

	parts := strings.Fields(requestLine)
	if len(parts) < 2 {
		writeHTTPError(conn, "400 Bad Request")
		return
	}

	method := strings.ToUpper(parts[0])
	target := parts[1]

	// ------------------------------------------------------------------ //
	// Phase 2 — Consume remaining headers (read until blank line)
	// ------------------------------------------------------------------ //

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
	}

	// ------------------------------------------------------------------ //
	// Phase 3 — Handle non-CONNECT methods with a human-readable response
	// ------------------------------------------------------------------ //

	if method != "CONNECT" {
		body := "reversproxy - HTTP CONNECT proxy\r\nSet HTTPS_PROXY=http://127.0.0.1:8080 to use.\r\n"
		resp := fmt.Sprintf(
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
			len(body), body,
		)
		_, _ = conn.Write([]byte(resp))
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 4 — Parse host:port from target
	// ------------------------------------------------------------------ //

	targetHost, targetPortStr, err := net.SplitHostPort(target)
	if err != nil {
		log.Debug("http connect proxy: bad target", "target", target, "err", err)
		writeHTTPError(conn, "400 Bad Request")
		return
	}
	targetPort, err := strconv.Atoi(targetPortStr)
	if err != nil {
		log.Debug("http connect proxy: bad port", "port", targetPortStr, "err", err)
		writeHTTPError(conn, "400 Bad Request")
		return
	}

	_ = conn.SetDeadline(time.Time{})

	log.Info("http connect proxy: CONNECT request",
		"target", target,
		"remote", conn.RemoteAddr(),
	)

	// ------------------------------------------------------------------ //
	// Phase 5 — Allocate mux channel and signal server
	// ------------------------------------------------------------------ //

	connID := uuid.New().String()

	// Allocate the channel BEFORE sending the message to avoid a race where
	// the server replies before we are listening on ReadyCh.
	ch, err := mux.NewChannel(connID)
	if err != nil {
		log.Warn("http connect proxy: mux.NewChannel failed", "connID", connID, "err", err)
		writeHTTPError(conn, "502 Bad Gateway")
		return
	}
	defer mux.Remove(connID)

	if err := cw.WriteMsg(protocol.MsgSOCKSConnect, protocol.SOCKSConnect{
		ConnID:     connID,
		TargetHost: targetHost,
		TargetPort: targetPort,
	}); err != nil {
		log.Warn("http connect proxy: failed to send SOCKSConnect", "connID", connID, "err", err)
		writeHTTPError(conn, "502 Bad Gateway")
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 6 — Wait for server's ready signal
	// ------------------------------------------------------------------ //

	var ready tunnel.SOCKSReadyResult
	select {
	case ready = <-ch.ReadyCh:
	case <-time.After(30 * time.Second):
		log.Warn("http connect proxy: timeout waiting for server dial", "connID", connID)
		writeHTTPError(conn, "504 Gateway Timeout")
		return
	case <-ctx.Done():
		writeHTTPError(conn, "503 Service Unavailable")
		return
	case <-ch.Done():
		writeHTTPError(conn, "502 Bad Gateway")
		return
	}

	if !ready.Success {
		log.Warn("http connect proxy: server dial failed",
			"connID", connID,
			"target", target,
			"err", ready.ErrMsg,
		)
		writeHTTPError(conn, "502 Bad Gateway")
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 7 — Send 200 Connection Established, then relay
	// ------------------------------------------------------------------ //

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		log.Debug("http connect proxy: failed to send 200", "connID", connID, "err", err)
		_ = cw.WriteMsg(protocol.MsgSOCKSClose, protocol.SOCKSClose{ConnID: connID})
		return
	}

	log.Info("http connect proxy: relay started", "connID", connID, "target", target)

	// outSend carries payloads from the local HTTP client to the mux writer.
	outSend := make(chan []byte, 64)
	muxWriterDone := make(chan struct{})

	// Goroutine A: local HTTP client → server via MsgSOCKSData
	go func() {
		defer close(outSend)
		// Use the buffered reader so we don't lose bytes already buffered
		// during header parsing.
		buf := make([]byte, 32*1024)
		for {
			n, rerr := br.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				outSend <- payload
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Goroutine B: server → local HTTP client via ch.Recv (MsgSOCKSData)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ch.Recv.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Mux writer: drains outSend → MsgSOCKSData to server
	go func() {
		defer close(muxWriterDone)
		for payload := range outSend {
			if err := cw.WriteMsg(protocol.MsgSOCKSData, protocol.SOCKSData{
				ConnID:  connID,
				Payload: payload,
			}); err != nil {
				for range outSend {
				} // drain to unblock goroutine A
				return
			}
		}
	}()

	// Wait until goroutine A has finished reading AND the mux writer has
	// sent all data to the server.
	<-muxWriterDone
	_ = cw.WriteMsg(protocol.MsgSOCKSClose, protocol.SOCKSClose{ConnID: connID})

	// Wait for the server to finish sending (goroutine B exits on DeliverClose).
	<-recvDone

	mux.Remove(connID)
	log.Info("http connect proxy: relay finished", "connID", connID)
}

// writeHTTPError sends a minimal HTTP error response and ignores write errors.
func writeHTTPError(conn net.Conn, status string) {
	body := status + "\r\n"
	resp := fmt.Sprintf(
		"HTTP/1.1 %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, len(body), body,
	)
	_, _ = conn.Write([]byte(resp))
}
