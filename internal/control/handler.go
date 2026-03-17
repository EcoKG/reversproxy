package control

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// HandleControlConn manages the lifecycle of a single control-plane connection:
// registration handshake → message loop → cleanup.
//
// In the new architecture the SERVER dials the CLIENT. HandleControlConn is
// called by the server after dialing; it sends a ClientRegister message to
// identify and authenticate itself, and then waits for the client's
// RegisterResp before entering the message loop.
//
// It blocks until the connection is closed, the parent context is cancelled,
// or the client sends a Disconnect message.
//
// mgr may be nil; when non-nil, tunnel management messages (RequestTunnel,
// OpenConnection) are handled. dataAddr is the address clients should dial
// for data connections (used in OpenConnection replies).
// ctrlConns may be nil; when non-nil the client's control connection is
// registered so the HTTP/HTTPS proxy can send OpenConnection messages.
func HandleControlConn(
	ctx context.Context,
	conn net.Conn,
	reg *ClientRegistry,
	authToken string,
	log *slog.Logger,
	mgr *tunnel.Manager,
	dataAddr string,
	ctrlConns ...*tunnel.ControlConnRegistry,
) {
	defer conn.Close()

	// Enable TCP keepalive so the OS detects half-open connections.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// ------------------------------------------------------------------ //
	// Registration phase
	//
	// New flow: SERVER sends ClientRegister → CLIENT validates → CLIENT sends RegisterResp.
	// HandleControlConn runs on the server side, so we SEND the register message
	// and then READ the response.
	// ------------------------------------------------------------------ //

	// Give the handshake 10 seconds to complete.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Error("failed to set registration deadline", "err", err)
		return
	}

	// Server sends its identity and auth token to the client.
	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: authToken,
		Name:      "server",
		Version:   "0.1.0",
	}); err != nil {
		log.Warn("failed to send ClientRegister to client", "err", err)
		return
	}

	// Wait for the client's acknowledgement.
	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("failed to read RegisterResp from client", "err", err)
		return
	}

	if env.Type != protocol.MsgRegisterResp {
		log.Warn("unexpected message type during registration", "type", env.Type)
		return
	}

	var resp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&resp); err != nil {
		log.Warn("failed to decode RegisterResp from client", "err", err)
		return
	}

	if resp.Status != "ok" {
		log.Warn("client rejected registration", "error", resp.Error, "addr", conn.RemoteAddr())
		return
	}

	// Create per-client context so heartbeat and handler goroutines can be
	// cancelled independently of the server root context.
	clientCtx, cancel := context.WithCancel(ctx)

	// Use the name returned by the client in the RegisterResp (ServerID field
	// carries the client's chosen name), falling back to the remote address.
	clientName := resp.ServerID
	if clientName == "" {
		clientName = conn.RemoteAddr().String()
	}

	client := reg.Register(clientName, conn.RemoteAddr().String(), conn, cancel)

	log.Info("client registered",
		"id", client.ID,
		"name", client.Name,
		"addr", client.Addr,
	)

	// Remove the registration deadline for the long-lived message loop.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Error("failed to clear deadline", "err", err)
		reg.Deregister(client.ID)
		cancel()
		return
	}

	// Register control connection in ControlConnRegistry if provided.
	var ccReg *tunnel.ControlConnRegistry
	if len(ctrlConns) > 0 && ctrlConns[0] != nil {
		ccReg = ctrlConns[0]
		ccReg.Register(client.ID, conn)
	}

	// Ensure cleanup runs regardless of how we exit.
	defer func() {
		if mgr != nil {
			mgr.RemoveTunnelsForClient(client.ID)
			mgr.RemoveHTTPTunnelsForClient(client.ID)
		}
		if ccReg != nil {
			ccReg.Deregister(client.ID)
		}
		reg.Deregister(client.ID)
		cancel()
	}()

	// Start the application-level heartbeat in its own goroutine.
	go StartHeartbeat(clientCtx, client, log)

	// ------------------------------------------------------------------ //
	// Message loop
	// ------------------------------------------------------------------ //
	for {
		// Bail out if the parent (or client) context has been cancelled.
		select {
		case <-clientCtx.Done():
			log.Info("client context cancelled, closing connection", "id", client.ID)
			return
		default:
		}

		env, err := protocol.ReadMessage(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Info("client disconnected", "id", client.ID, "err", err)
			} else {
				// Net errors after context cancellation are expected; downgrade them.
				select {
				case <-clientCtx.Done():
					log.Info("client disconnected", "id", client.ID, "err", err)
				default:
					log.Warn("client disconnected", "id", client.ID, "err", err)
				}
			}
			return
		}

		switch env.Type {
		case protocol.MsgPong:
			var pong protocol.Pong
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&pong); err != nil {
				log.Warn("failed to decode Pong", "id", client.ID, "err", err)
				continue
			}
			client.LastHeartbeatAt = time.Now()

		case protocol.MsgDisconnect:
			var disc protocol.Disconnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&disc); err != nil {
				log.Warn("failed to decode Disconnect", "id", client.ID, "err", err)
			} else {
				log.Info("client requested disconnect",
					"id", client.ID,
					"reason", disc.Reason,
				)
			}
			_ = protocol.WriteMessage(conn, protocol.MsgDisconnectAck, protocol.DisconnectAck{})
			return

		case protocol.MsgRequestTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestTunnel", "id", client.ID)
				continue
			}
			handleRequestTunnel(clientCtx, env, client, conn, mgr, dataAddr, log)

		case protocol.MsgRequestHTTPTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestHTTPTunnel", "id", client.ID)
				continue
			}
			handleRequestHTTPTunnel(env, client, conn, mgr, dataAddr, log)

		case protocol.MsgRequestHTTPSTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestHTTPSTunnel", "id", client.ID)
				continue
			}
			handleRequestHTTPSTunnel(env, client, conn, mgr, dataAddr, log)

		case protocol.MsgSOCKSReady:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring MsgSOCKSReady", "id", client.ID)
				continue
			}
			handleSOCKSReady(env, client, mgr, log)

		default:
			log.Warn("unhandled message type", "id", client.ID, "type", env.Type)
		}
	}
}

// handleSOCKSReady processes a MsgSOCKSReady message from a client.
// It forwards the dial result to the SOCKS5 handler waiting on the pending slot.
func handleSOCKSReady(
	env *protocol.Envelope,
	client *Client,
	mgr *tunnel.Manager,
	log *slog.Logger,
) {
	var ready protocol.SOCKSReady
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
		log.Warn("failed to decode SOCKSReady", "id", client.ID, "err", err)
		return
	}

	if err := mgr.FulfillSOCKS(ready.ConnID, ready.Success, ready.Error); err != nil {
		log.Warn("FulfillSOCKS failed", "id", client.ID, "connID", ready.ConnID, "err", err)
	}
}

// handleRequestHTTPTunnel processes a MsgRequestHTTPTunnel from a client.
// It registers the hostname in the TunnelManager's HTTP routing table.
func handleRequestHTTPTunnel(
	env *protocol.Envelope,
	client *Client,
	conn net.Conn,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestHTTPTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestHTTPTunnel", "id", client.ID, "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Status: "error",
			Error:  "malformed RequestHTTPTunnel payload",
		})
		return
	}

	tunnelID := uuid.New().String()
	mgr.AddHTTPTunnel(tunnelID, client.ID, req.Hostname, req.LocalHost, req.LocalPort)

	log.Info("HTTP tunnel registered",
		"tunnelID", tunnelID,
		"clientID", client.ID,
		"hostname", req.Hostname,
		"localHost", req.LocalHost,
		"localPort", req.LocalPort,
	)

	_ = protocol.WriteMessage(conn, protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
		Hostname:       req.Hostname,
		TunnelID:       tunnelID,
		ServerDataAddr: dataAddr,
		Status:         "ok",
	})
}

// handleRequestHTTPSTunnel processes a MsgRequestHTTPSTunnel from a client.
// It registers the SNI hostname in the TunnelManager's HTTPS routing table.
func handleRequestHTTPSTunnel(
	env *protocol.Envelope,
	client *Client,
	conn net.Conn,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestHTTPSTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestHTTPSTunnel", "id", client.ID, "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Status: "error",
			Error:  "malformed RequestHTTPSTunnel payload",
		})
		return
	}

	tunnelID := uuid.New().String()
	mgr.AddHTTPSTunnel(tunnelID, client.ID, req.Hostname, req.LocalHost, req.LocalPort)

	log.Info("HTTPS tunnel registered",
		"tunnelID", tunnelID,
		"clientID", client.ID,
		"hostname", req.Hostname,
		"localHost", req.LocalHost,
		"localPort", req.LocalPort,
	)

	_ = protocol.WriteMessage(conn, protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
		Hostname:       req.Hostname,
		TunnelID:       tunnelID,
		ServerDataAddr: dataAddr,
		Status:         "ok",
	})
}

// handleRequestTunnel processes a MsgRequestTunnel from a client.
// It opens a public TCP listener and sends back a TunnelResp.
func handleRequestTunnel(
	ctx context.Context,
	env *protocol.Envelope,
	client *Client,
	conn net.Conn,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestTunnel", "id", client.ID, "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgTunnelResp, protocol.TunnelResp{
			Status: "error",
			Error:  "malformed RequestTunnel payload",
		})
		return
	}

	// Choose the listen address.
	listenAddr := fmt.Sprintf(":%d", req.RequestedPort) // 0 → OS picks a port

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Error("failed to open public listener", "id", client.ID, "addr", listenAddr, "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgTunnelResp, protocol.TunnelResp{
			Status: "error",
			Error:  fmt.Sprintf("could not listen on %s: %v", listenAddr, err),
		})
		return
	}

	publicPort := ln.Addr().(*net.TCPAddr).Port
	tunnelID := uuid.New().String()

	entry := mgr.AddTunnel(tunnelID, client.ID, req.LocalHost, req.LocalPort, publicPort, ln)

	log.Info("tunnel created",
		"tunnelID", tunnelID,
		"clientID", client.ID,
		"publicPort", publicPort,
		"localHost", req.LocalHost,
		"localPort", req.LocalPort,
	)

	// Reply to the client with the assigned tunnel info.
	if err := protocol.WriteMessage(conn, protocol.MsgTunnelResp, protocol.TunnelResp{
		TunnelID:       tunnelID,
		PublicPort:     publicPort,
		ServerDataAddr: dataAddr,
		Status:         "ok",
	}); err != nil {
		log.Error("failed to send TunnelResp", "id", client.ID, "err", err)
		_ = ln.Close()
		return
	}

	// Start the public listener goroutine.
	go tunnel.StartPublicListener(ctx, entry, conn, mgr, log)
}
