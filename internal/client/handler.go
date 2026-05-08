// Package client provides the shared client-side logic for the reverse proxy.
// It is used by both the standard CLI client (cmd/client) and any GUI wrapper.
package client

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log/slog"
	"time"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// HandleServerConn manages a single connection from a proxy server.
// It:
//  1. Performs the reversed registration handshake.
//  2. Re-registers all configured tunnels on this server.
//  3. Runs the message loop until the connection is lost or ctx is cancelled.
//
// Each session has its own writer and mux so multiple servers can be served
// concurrently. Client-originated SOCKS / HTTP CONNECT / port-forward traffic
// is routed via ServerPool.Pick which selects one session round-robin.
func HandleServerConn(
	ctx context.Context,
	session *ServerSession,
	authToken, name string,
	cfg *reconnect.ClientConfig,
	log *slog.Logger,
) {
	conn := session.Conn
	writer := session.Writer
	mux := session.Mux

	defer conn.Close()
	defer mux.CloseAll()

	// ------------------------------------------------------------------ //
	// Registration handshake (reversed)
	// Server sends ClientRegister → client validates → client sends RegisterResp
	// ------------------------------------------------------------------ //

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

	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: name,
	}); err != nil {
		log.Error("failed to send RegisterResp", "err", err)
		return
	}

	// Capture server-supplied identity (carried in ClientRegister.Name field
	// per the reversed handshake) so the GUI/admin can show it.
	if msg.Name != "" {
		session.ServerName = msg.Name
	}

	log.Info("registered with server",
		"remote", conn.RemoteAddr(),
		"client_name", name,
		"server_name", msg.Name,
	)

	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Error("failed to clear deadline", "err", err)
		return
	}

	// ------------------------------------------------------------------ //
	// Re-register all tunnels with this server.
	// ------------------------------------------------------------------ //
	tunnelDataAddrs := make(map[string]string)
	var serverDataAddr string

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
		fmt.Printf("Tunnel: 0.0.0.0:%d → %s:%d (server=%s)\n",
			tresp.PublicPort, tc.LocalHost, tc.LocalPort, session.Addr)
	}

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
		fmt.Printf("HTTP Tunnel: http://%s → %s:%d (server=%s)\n",
			hresp.Hostname, hc.LocalHost, hc.LocalPort, session.Addr)
	}

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
		fmt.Printf("HTTPS Tunnel: https://%s → %s:%d (server=%s)\n",
			hresp.Hostname, hc.LocalHost, hc.LocalPort, session.Addr)
	}

	// ------------------------------------------------------------------ //
	// Per-session shutdown signal: if the parent ctx is cancelled, send a
	// disconnect frame and force the read loop to unblock.
	// ------------------------------------------------------------------ //
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	go func() {
		<-connCtx.Done()
		if ctx.Err() != nil {
			log.Info("signal received, sending disconnect", "server", session.Addr)
			_ = writer.WriteMsg(protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})
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
			log.Warn("connection to server lost", "server", session.Addr, "err", err)
			return
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				log.Warn("failed to decode Ping", "err", err)
				continue
			}
			if err := writer.WriteMsg(protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
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
				log.Info("server requested disconnect", "reason", disc.Reason, "server", session.Addr)
			}
			_ = writer.WriteMsg(protocol.MsgDisconnectAck, protocol.DisconnectAck{})
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

		case protocol.MsgSOCKSReady:
			var ready protocol.SOCKSReady
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
				log.Warn("failed to decode SOCKSReady", "err", err)
				continue
			}
			if err := mux.DeliverReady(ready.ConnID, ready.Success, ready.Error); err != nil {
				log.Debug("SOCKSReady deliver failed", "connID", ready.ConnID, "err", err)
			}

		case protocol.MsgSOCKSData:
			var sd protocol.SOCKSData
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
				log.Warn("failed to decode SOCKSData", "err", err)
				continue
			}
			if err := mux.Deliver(sd.ConnID, sd.Payload); err != nil {
				log.Debug("SOCKSData deliver failed", "connID", sd.ConnID, "err", err)
			}

		case protocol.MsgSOCKSClose:
			var sc protocol.SOCKSClose
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSClose", "err", err)
				continue
			}
			mux.DeliverClose(sc.ConnID)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}
