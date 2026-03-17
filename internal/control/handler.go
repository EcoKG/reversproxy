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
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// HandleControlConn manages the lifecycle of a single control-plane connection:
// registration handshake → message loop → cleanup.
//
// It blocks until the connection is closed, the parent context is cancelled,
// or the client sends a Disconnect message.
//
// mgr may be nil; when non-nil, tunnel management messages (RequestTunnel,
// OpenConnection) are handled. dataAddr is the address clients should dial
// for data connections (used in OpenConnection replies).
func HandleControlConn(
	ctx context.Context,
	conn net.Conn,
	reg *ClientRegistry,
	authToken string,
	log *slog.Logger,
	mgr *tunnel.Manager,
	dataAddr string,
) {
	defer conn.Close()

	// Enable TCP keepalive so the OS detects half-open connections.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// ------------------------------------------------------------------ //
	// Registration phase
	// ------------------------------------------------------------------ //

	// Give the client 10 seconds to send ClientRegister.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Error("failed to set registration deadline", "err", err)
		return
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("failed to read registration message", "err", err)
		return
	}

	if env.Type != protocol.MsgClientRegister {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "expected ClientRegister",
		})
		log.Warn("unexpected message type during registration", "type", env.Type)
		return
	}

	var msg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&msg); err != nil {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "malformed ClientRegister payload",
		})
		log.Warn("failed to decode ClientRegister", "err", err)
		return
	}

	if msg.AuthToken != authToken {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "invalid token",
		})
		log.Warn("registration rejected: invalid token", "addr", conn.RemoteAddr())
		return
	}

	// Create per-client context so heartbeat and handler goroutines can be
	// cancelled independently of the server root context.
	clientCtx, cancel := context.WithCancel(ctx)

	client := reg.Register(msg.Name, conn.RemoteAddr().String(), conn, cancel)

	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		AssignedClientID: client.ID,
		Status:           "ok",
	}); err != nil {
		cancel()
		log.Error("failed to send RegisterResp", "err", err)
		return
	}

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

	// Ensure cleanup runs regardless of how we exit.
	defer func() {
		if mgr != nil {
			mgr.RemoveTunnelsForClient(client.ID)
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

		default:
			log.Warn("unhandled message type", "id", client.ID, "type", env.Type)
		}
	}
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
