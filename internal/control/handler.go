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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// serverCtrlWriter serialises writes to the control connection from multiple
// concurrent SOCKS relay goroutines.
type serverCtrlWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (w *serverCtrlWriter) Write(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return protocol.WriteMessage(w.conn, msgType, payload)
}

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
//
// A per-connection SOCKSMux is created internally to multiplex any SOCKS5
// channels that the client initiates over this control connection.
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

	// Server-side SOCKS mux — one per control connection.
	// Each entry represents an internet target the server has dialled on behalf
	// of the client's local SOCKS5 user.
	serverMux := tunnel.NewSOCKSMux()
	cw := &serverCtrlWriter{conn: conn}

	// Ensure cleanup runs regardless of how we exit.
	defer func() {
		// Close all active SOCKS channels so relay goroutines exit cleanly.
		serverMux.CloseAll()
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

		// ------------------------------------------------------------------
		// Reversed SOCKS5 messages (Phase 4 reversed):
		// Client sends MsgSOCKSConnect → server dials internet → relay via mux.
		// ------------------------------------------------------------------

		case protocol.MsgSOCKSConnect:
			var sc protocol.SOCKSConnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSConnect", "id", client.ID, "err", err)
				continue
			}
			handleServerSOCKSConnect(clientCtx, sc, cw, serverMux, log)

		case protocol.MsgSOCKSData:
			var sd protocol.SOCKSData
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
				log.Warn("failed to decode SOCKSData", "id", client.ID, "err", err)
				continue
			}
			if err := serverMux.Deliver(sd.ConnID, sd.Payload); err != nil {
				log.Debug("SOCKSData deliver failed", "connID", sd.ConnID, "err", err)
			}

		case protocol.MsgSOCKSClose:
			var sc protocol.SOCKSClose
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSClose", "id", client.ID, "err", err)
				continue
			}
			serverMux.DeliverClose(sc.ConnID)

		default:
			log.Warn("unhandled message type", "id", client.ID, "type", env.Type)
		}
	}
}

// handleServerSOCKSConnect is called when the server receives a MsgSOCKSConnect
// from the client.  It dials the internet target, creates a server-side mux
// channel, and relays data bidirectionally over MsgSOCKSData frames.
func handleServerSOCKSConnect(
	ctx context.Context,
	sc protocol.SOCKSConnect,
	cw *serverCtrlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	log = log.With("connID", sc.ConnID, "target", fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort))

	go func() {
		targetAddr := fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort)

		// Allocate the server-side channel before dialling so that incoming
		// MsgSOCKSData frames (if the client sends them early) have a home.
		ch, err := mux.NewChannel(sc.ConnID)
		if err != nil {
			log.Warn("server: mux.NewChannel failed", "err", err)
			_ = cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
				ConnID:  sc.ConnID,
				Success: false,
				Error:   err.Error(),
			})
			return
		}
		defer mux.Remove(sc.ConnID)

		// Dial the internet target (server has internet access).
		targetConn, err := net.DialTimeout("tcp", targetAddr, 15*time.Second)
		if err != nil {
			log.Warn("server: failed to dial target", "err", err)
			_ = cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
				ConnID:  sc.ConnID,
				Success: false,
				Error:   err.Error(),
			})
			return
		}
		defer targetConn.Close()

		// Notify the client that the dial succeeded.
		if err := cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  sc.ConnID,
			Success: true,
		}); err != nil {
			log.Warn("server: failed to send SOCKSReady", "err", err)
			return
		}

		log.Info("server: SOCKS relay started")

		// outSend carries payloads from the internet target to the mux writer.
		outSend := make(chan []byte, 64)
		muxWriterDone := make(chan struct{})

		// Goroutine A: internet target → client
		// Reads from targetConn; pumps payloads into outSend.
		// Closes outSend when target closes the connection / sends FIN.
		go func() {
			defer close(outSend)
			buf := make([]byte, 32*1024)
			for {
				n, err := targetConn.Read(buf)
				if n > 0 {
					payload := make([]byte, n)
					copy(payload, buf[:n])
					outSend <- payload
				}
				if err != nil {
					return
				}
			}
		}()

		// Goroutine B: client → internet target
		// Reads from ch.Recv (MsgSOCKSData from client), writes to targetConn.
		// Exits when ch.Recv returns EOF (ch.recvW closed by mux.Remove /
		// DeliverClose triggered by the client's MsgSOCKSClose).
		recvDone := make(chan struct{})
		go func() {
			defer close(recvDone)
			_, _ = io.Copy(targetConn, ch.Recv)
		}()

		// Mux writer: drains outSend → MsgSOCKSData frames to client.
		go func() {
			defer close(muxWriterDone)
			for payload := range outSend {
				if err := cw.Write(protocol.MsgSOCKSData, protocol.SOCKSData{
					ConnID:  sc.ConnID,
					Payload: payload,
				}); err != nil {
					for range outSend {
					}
					return
				}
			}
		}()

		// Half-close sequence (mirror of client side):
		//   1. Wait until goroutine A and mux writer have flushed all data.
		//   2. Send MsgSOCKSClose — client will unblock goroutine B on its side.
		//   3. The client's MsgSOCKSClose arrives here via the message loop →
		//      serverMux.DeliverClose(connID) → ch.recvW closed → goroutine B
		//      on this side gets EOF and exits.
		//   4. Wait for goroutine B.
		<-muxWriterDone
		_ = cw.Write(protocol.MsgSOCKSClose, protocol.SOCKSClose{ConnID: sc.ConnID})

		// Wait for goroutine B to exit (triggered by client's MsgSOCKSClose).
		<-recvDone

		// Cleanup.
		mux.Remove(sc.ConnID)

		log.Info("server: SOCKS relay finished")
	}()
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
