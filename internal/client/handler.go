// Package client provides the shared client-side logic for the reverse proxy.
// It is used by both the standard CLI client (cmd/client) and any GUI wrapper.
package client

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// ConnWriter wraps a net.Conn with a mutex so that all writes from concurrent
// goroutines (SOCKS relay goroutines + message loop) are serialised.
// It implements socks.ControlWriter.
// The underlying connection can be swapped via SwapConn when the server reconnects.
type ConnWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

// WriteMsg writes a protocol message to the current connection.
// Returns an error if no connection is active.
func (w *ConnWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn == nil {
		return fmt.Errorf("no active control connection")
	}
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// SwapConn replaces the underlying connection (called on server reconnect).
func (w *ConnWriter) SwapConn(c net.Conn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = c
}

// ClearConn sets the connection to nil (called when connection is lost).
func (w *ConnWriter) ClearConn() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = nil
}

// HandleServerConn manages a single connection from the proxy server.
// It:
//  1. Performs the reversed registration handshake.
//  2. Re-registers all configured tunnels.
//  3. Runs the message loop until the connection is lost or ctx is cancelled.
//
// The SOCKS5/HTTP CONNECT proxies are started once by the caller and share the
// swappable sharedWriter and clientMux passed here.
func HandleServerConn(
	ctx context.Context,
	conn net.Conn,
	authToken, name string,
	cfg *reconnect.ClientConfig,
	sharedWriter *ConnWriter,
	clientMux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	defer conn.Close()

	// ------------------------------------------------------------------ //
	// Registration handshake (reversed)
	// Server sends ClientRegister → client validates → client sends RegisterResp
	// ------------------------------------------------------------------ //

	// Give the handshake 10 seconds to complete.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Error("failed to set registration deadline", "err", err)
		return
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("failed to read registration message from server", "err", err)
		return
	}

	if env.Type != protocol.MsgClientRegister {
		log.Warn("unexpected message type during handshake", "type", env.Type)
		return
	}

	var msg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&msg); err != nil {
		log.Warn("failed to decode ClientRegister from server", "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "malformed ClientRegister payload",
		})
		return
	}

	if msg.AuthToken != authToken {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "invalid token",
		})
		log.Warn("registration rejected: invalid token from server", "remote", conn.RemoteAddr())
		return
	}

	// Send RegisterResp with the client's name in ServerID so the server knows
	// which client it has connected to.
	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: name, // client's name carried in ServerID field
	}); err != nil {
		log.Error("failed to send RegisterResp", "err", err)
		return
	}

	log.Info("registered with server", "remote", conn.RemoteAddr(), "client_name", name)

	// Remove the registration deadline.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Error("failed to clear deadline", "err", err)
		return
	}

	// ------------------------------------------------------------------ //
	// Re-register all tunnels
	// ------------------------------------------------------------------ //
	tunnelDataAddrs := make(map[string]string)
	var serverDataAddr string // first data addr learned from any tunnel registration

	for _, tc := range cfg.Tunnels {
		req := protocol.RequestTunnel{
			LocalHost:     tc.LocalHost,
			LocalPort:     tc.LocalPort,
			RequestedPort: tc.RequestedPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, req); err != nil {
			log.Warn("send RequestTunnel failed", "err", err)
			return
		}

		tenv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read TunnelResp failed", "err", err)
			return
		}
		if tenv.Type != protocol.MsgTunnelResp {
			log.Warn("expected TunnelResp", "got", tenv.Type)
			return
		}

		var tresp protocol.TunnelResp
		if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
			log.Warn("decode TunnelResp failed", "err", err)
			return
		}

		if tresp.Status != "ok" {
			log.Warn("tunnel request failed", "err", tresp.Error)
			return
		}

		tunnelDataAddrs[tresp.TunnelID] = tresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = tresp.ServerDataAddr
		}
		log.Info("tunnel established",
			"tunnelID", tresp.TunnelID,
			"publicPort", tresp.PublicPort,
			"serverDataAddr", tresp.ServerDataAddr,
		)
		fmt.Printf("Tunnel: 0.0.0.0:%d → %s:%d\n", tresp.PublicPort, tc.LocalHost, tc.LocalPort)
	}

	// Register HTTP tunnels.
	for _, hc := range cfg.HTTPTunnels {
		req := protocol.RequestHTTPTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPTunnel, req); err != nil {
			log.Warn("send RequestHTTPTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTP tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTP tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTP Tunnel: http://%s → %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	// Register HTTPS tunnels.
	for _, hc := range cfg.HTTPSTunnels {
		req := protocol.RequestHTTPSTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPSTunnel, req); err != nil {
			log.Warn("send RequestHTTPSTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp (HTTPS)", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTPS tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTPS tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTPS Tunnel: https://%s → %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	// ------------------------------------------------------------------ //
	// Graceful shutdown for this connection
	// ------------------------------------------------------------------ //
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	go func() {
		<-connCtx.Done()
		if ctx.Err() != nil {
			// Global shutdown — send disconnect
			log.Info("signal received, sending disconnect")
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	}()

	// ------------------------------------------------------------------ //
	// Message loop
	// ------------------------------------------------------------------ //
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		env, err := protocol.ReadMessage(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("connection to server lost", "err", err)
			return
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				log.Warn("failed to decode Ping", "err", err)
				continue
			}
			if err := sharedWriter.WriteMsg(protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn("send Pong failed", "err", err)
				return
			}
			log.Debug("pong sent", "seq", ping.Seq)

		case protocol.MsgDisconnect:
			var disc protocol.Disconnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&disc); err != nil {
				log.Warn("failed to decode Disconnect from server", "err", err)
			} else {
				log.Info("server requested disconnect", "reason", disc.Reason)
			}
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnectAck, protocol.DisconnectAck{})
			return

		case protocol.MsgOpenConnection:
			var openConn protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&openConn); err != nil {
				log.Warn("failed to decode OpenConnection", "err", err)
				continue
			}
			dataAddr, ok := tunnelDataAddrs[openConn.TunnelID]
			if !ok {
				log.Warn("received OpenConnection for unknown tunnelID",
					"tunnelID", openConn.TunnelID,
					"known_tunnels", len(tunnelDataAddrs),
				)
				continue
			}
			tunnel.HandleOpenConnection(openConn, dataAddr, log)

		// ---- Reversed SOCKS5 messages (Phase 4 reversed) ----
		// The server sends these back to us after we sent MsgSOCKSConnect.

		case protocol.MsgSOCKSReady:
			var ready protocol.SOCKSReady
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
				log.Warn("failed to decode SOCKSReady", "err", err)
				continue
			}
			if err := clientMux.DeliverReady(ready.ConnID, ready.Success, ready.Error); err != nil {
				log.Debug("SOCKSReady deliver failed", "connID", ready.ConnID, "err", err)
			}

		case protocol.MsgSOCKSData:
			var sd protocol.SOCKSData
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
				log.Warn("failed to decode SOCKSData", "err", err)
				continue
			}
			if err := clientMux.Deliver(sd.ConnID, sd.Payload); err != nil {
				log.Debug("SOCKSData deliver failed", "connID", sd.ConnID, "err", err)
			}

		case protocol.MsgSOCKSClose:
			var sc protocol.SOCKSClose
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSClose", "err", err)
				continue
			}
			clientMux.DeliverClose(sc.ConnID)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}
