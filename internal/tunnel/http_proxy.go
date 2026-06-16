package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/google/uuid"
)

// maxHTTPHeaderBytes bounds the request line + headers of an inbound proxied
// HTTP request so a client cannot exhaust memory by streaming header bytes
// (http.ReadRequest applies no limit of its own). The relay phase reads the
// connection directly and is unaffected.
const maxHTTPHeaderBytes = 64 * 1024

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
func StartHTTPProxy(ctx context.Context, addr string, mgr *Manager, ctrlConns *ControlConnRegistry, dataAddr string, log *slog.Logger, lim ...*Limiter) error {
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

		var rl *Limiter
		if len(lim) > 0 {
			rl = lim[0]
		}

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

			if rl != nil {
				ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
				if !rl.Allow(ip) {
					log.Warn("HTTP proxy: rate limited", "ip", ip)
					conn.Close()
					continue
				}
				if !rl.Acquire() {
					log.Warn("HTTP proxy: max concurrent connections", "ip", ip)
					conn.Close()
					continue
				}
				go func() {
					defer rl.Release()
					handleHTTPConn(ctx, conn, mgr, ctrlConns, dataAddr, log)
				}()
				continue
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
	_ = extConn.SetDeadline(time.Now().Add(config.ProxyReadTimeout))

	// Bound the header read; the relay phase reads extConn directly so it is
	// unaffected by this cap.
	lr := &io.LimitedReader{R: extConn, N: maxHTTPHeaderBytes}
	br := bufio.NewReader(lr)

	req, err := http.ReadRequest(br)
	if err != nil {
		code, msg := http.StatusBadRequest, "Bad Request"
		if lr.N <= 0 {
			code, msg = http.StatusRequestHeaderFieldsTooLarge, "Request Header Fields Too Large"
		}
		log.Warn("HTTP proxy: failed to read request", "err", err, "remote", extConn.RemoteAddr())
		writeHTTPError(extConn, code, msg)
		extConn.Close()
		return
	}

	// Extract hostname (strip port if present) and normalise to lower case so it
	// matches the lowercased key stored at registration.
	host := req.Host
	if h, _, err2 := net.SplitHostPort(host); err2 == nil {
		host = h
	}
	host = strings.ToLower(host)

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

	// Look up the client's control-connection writer (serialised).
	clientWriter, ok := ctrlConns.Get(entry.ClientID)
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

	// Strip all source-identifying headers before forwarding.
	// The proxy server is the sole communicating party — the backend service
	// must have no knowledge of the original external client's identity or path.
	stripSourceHeaders(req)

	// Rebuild the raw HTTP request bytes from the parsed request so we can
	// replay them into the data connection. We use a pipe: write the request
	// back to a net.Conn-compatible buffer.
	var rawReqBuf bytes.Buffer
	_ = req.Write(&rawReqBuf)

	// If the bufio.Reader has buffered bytes beyond the HTTP request (e.g.,
	// pipelined data or a request body), append them so nothing is lost.
	if br.Buffered() > 0 {
		remaining, _ := br.Peek(br.Buffered())
		rawReqBuf.Write(remaining)
		// Discard the peeked bytes from the reader so they aren't read again.
		_, _ = br.Discard(len(remaining))
	}

	// Register the pending connection (external conn) before sending OpenConnection.
	pending := mgr.RegisterPending(connID, extConn)

	openMsg := protocol.OpenConnection{
		TunnelID:  entry.ID,
		ConnID:    connID,
		LocalHost: entry.LocalHost,
		LocalPort: entry.LocalPort,
		DataToken: PendingToken(pending),
	}
	if err := clientWriter.Write(protocol.MsgOpenConnection, openMsg); err != nil {
		log.Warn("HTTP proxy: failed to send OpenConnection", "connID", connID, "err", err)
		mgr.CancelPending(connID)
		extConn.Close()
		return
	}

	// Relay synchronously so that, when a concurrency Limiter is configured, the
	// accept loop's deferred rl.Release() fires only after the relay finishes
	// (not at hand-off) — keeping the in-flight count accurate. handleHTTPConn is
	// always invoked in its own goroutine, so blocking here is safe.
	relayHTTPConn(ctx, pending, connID, mgr, rawReqBuf.Bytes(), log)
}

// relayHTTPConn waits for the client's data connection, replays the raw HTTP
// request bytes, then relays bidirectionally.
func relayHTTPConn(ctx context.Context, pending *pendingConn, connID string, mgr *Manager, rawReq []byte, log *slog.Logger) {
	waitDone := make(chan net.Conn, 1)
	go func() {
		waitDone <- WaitReady(pending)
	}()

	var dataConn net.Conn
	select {
	case dataConn = <-waitDone:
	case <-time.After(config.DataConnWaitTimeout):
		log.Warn("HTTP proxy: timeout waiting for data conn", "connID", connID)
		mgr.CancelPending(connID)
		PendingExtConn(pending).Close()
		return
	case <-ctx.Done():
		mgr.CancelPending(connID)
		PendingExtConn(pending).Close()
		return
	}

	if dataConn == nil {
		// Pending entry was cancelled concurrently; nothing to relay.
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

	RelayBiDirectional(ctx, extConn, dataConn)

	log.Info("HTTP proxy: relay finished", "connID", connID)
}

// sourceHeaders lists headers that could reveal the original client's identity
// or network path, or that are proxy hop-by-hop headers. All are removed before
// forwarding through the tunnel so the backend sees only the proxy.
var sourceHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
	"X-Original-Forwarded-For",
	"Forwarded",
	"Via",
	"X-Real-Ip",
	"X-Client-Ip",
	"Client-Ip",
	"X-Cluster-Client-Ip",
	"X-Originating-Ip",
	"X-Proxyuser-Ip",
	"Proxy-Client-Ip",
	"Wl-Proxy-Client-Ip",
	"True-Client-Ip",
	"Cf-Connecting-Ip",
	"Cf-Connecting-Ipv6",
	"Cf-Ipcountry",
	"Cf-Ray",
	"Fastly-Client-Ip",
	"Fastly-Ssl",
	// Proxy hop-by-hop headers that must not be forwarded.
	"Proxy-Authorization",
	"Proxy-Connection",
}

// stripSourceHeaders removes all headers that could reveal the original
// client's IP address or network path. After this call the backend service
// has no knowledge of the external caller — only the proxy is visible.
func stripSourceHeaders(req *http.Request) {
	for _, h := range sourceHeaders {
		req.Header.Del(h)
	}
}

// writeHTTPError writes a minimal HTTP error response to conn.
func writeHTTPError(conn net.Conn, code int, msg string) {
	body := fmt.Sprintf("%d %s\n", code, msg)
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
	_, _ = conn.Write([]byte(resp))
}
