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
	"strings"
	"syscall"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
	"github.com/google/uuid"
)

// allowPrivateSOCKSTargets, when true, disables the SSRF egress guard so the
// SOCKS/HTTP-CONNECT/port-forward exit node may dial loopback, private, and
// link-local addresses. It is set once at startup; the default (false) blocks
// those ranges.
var allowPrivateSOCKSTargets bool

// SetAllowPrivateSOCKSTargets configures the exit-node SSRF policy. Call once at
// startup before any control connection is handled.
func SetAllowPrivateSOCKSTargets(allow bool) { allowPrivateSOCKSTargets = allow }

// isBlockedTargetIP reports whether ip is in a range the exit node must refuse
// to reach (SSRF guard): loopback, private (RFC1918/RFC4193 ULA), link-local,
// unspecified, or multicast — including the cloud metadata range 169.254/16.
func isBlockedTargetIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// newSOCKSDialer returns a dialer whose Control hook enforces the SSRF guard on
// the ACTUAL resolved IP immediately before connect, which also defeats
// DNS-rebinding/TOCTOU (a hostname that resolves to a public IP at validation
// time but a private IP at dial time is still blocked here).
func newSOCKSDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivateSOCKSTargets {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isBlockedTargetIP(ip) {
				return fmt.Errorf("ssrf guard: refusing to dial internal/non-routable address %q", address)
			}
			return nil
		},
	}
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
		_ = tc.SetKeepAlivePeriod(config.TCPKeepAlivePeriod)
	}

	// ------------------------------------------------------------------ //
	// Registration phase
	//
	// New flow: SERVER sends ClientRegister → CLIENT validates → CLIENT sends RegisterResp.
	// HandleControlConn runs on the server side, so we SEND the register message
	// and then READ the response.
	// ------------------------------------------------------------------ //

	// Give the handshake 10 seconds to complete.
	if err := conn.SetDeadline(time.Now().Add(config.HandshakeTimeout)); err != nil {
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

	// All writes to this control conn go through the per-connection serialising
	// writer created in reg.Register, so heartbeat, proxy OpenConnection sends,
	// control responses, and SOCKS frames cannot interleave on the wire.
	cw := client.Writer

	// Register control connection in ControlConnRegistry if provided.
	var ccReg *tunnel.ControlConnRegistry
	if len(ctrlConns) > 0 && ctrlConns[0] != nil {
		ccReg = ctrlConns[0]
		ccReg.Register(client.ID, cw)
	}

	// Server-side SOCKS mux — one per control connection.
	// Each entry represents an internet target the server has dialled on behalf
	// of the client's local SOCKS5 user.
	serverMux := tunnel.NewSOCKSMux()

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

	// Interrupt the blocking ReadMessage as soon as the client context is
	// cancelled (e.g. heartbeat timeout), so a dead client is torn down
	// immediately instead of after the current MessageReadTimeout window.
	go func() {
		<-clientCtx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()

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

		conn.SetReadDeadline(time.Now().Add(config.MessageReadTimeout))
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
			client.SetLastHeartbeat(time.Now())

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
			if err := cw.Write(protocol.MsgDisconnectAck, protocol.DisconnectAck{}); err != nil {
				log.Debug("failed to send DisconnectAck", "id", client.ID, "err", err)
			}
			return

		case protocol.MsgRequestTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestTunnel", "id", client.ID)
				continue
			}
			handleRequestTunnel(clientCtx, env, client, cw, mgr, dataAddr, log)

		case protocol.MsgRequestHTTPTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestHTTPTunnel", "id", client.ID)
				continue
			}
			handleRequestHTTPTunnel(env, client, cw, mgr, dataAddr, log)

		case protocol.MsgRequestHTTPSTunnel:
			if mgr == nil {
				log.Warn("tunnel manager not configured, ignoring RequestHTTPSTunnel", "id", client.ID)
				continue
			}
			handleRequestHTTPSTunnel(env, client, cw, mgr, dataAddr, log)

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
	cw *tunnel.CtrlConnWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	log = log.With("connID", sc.ConnID, "target", fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort))

	go func() {
		if err := protocol.ValidateTarget(sc.TargetHost, sc.TargetPort, 1); err != nil {
			log.Warn("server: invalid SOCKS target", "err", err)
			if werr := cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
				ConnID:  sc.ConnID,
				Success: false,
				Error:   err.Error(),
			}); werr != nil {
				log.Debug("server: failed to send SOCKSReady failure", "err", werr)
			}
			return
		}

		targetAddr := fmt.Sprintf("%s:%d", sc.TargetHost, sc.TargetPort)

		// Allocate the server-side channel before dialling so that incoming
		// MsgSOCKSData frames (if the client sends them early) have a home.
		ch, err := mux.NewChannel(sc.ConnID)
		if err != nil {
			log.Warn("server: mux.NewChannel failed", "err", err)
			if werr := cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
				ConnID:  sc.ConnID,
				Success: false,
				Error:   err.Error(),
			}); werr != nil {
				log.Debug("server: failed to send SOCKSReady failure", "err", werr)
			}
			return
		}
		defer mux.Remove(sc.ConnID)

		// Dial the internet target (server has internet access). The dialer's
		// Control hook enforces the SSRF egress guard on the resolved IP.
		targetConn, err := newSOCKSDialer(config.SOCKSDialTimeout).DialContext(ctx, "tcp", targetAddr)
		if err != nil {
			log.Warn("server: failed to dial target", "err", err)
			if werr := cw.Write(protocol.MsgSOCKSReady, protocol.SOCKSReady{
				ConnID:  sc.ConnID,
				Success: false,
				Error:   err.Error(),
			}); werr != nil {
				log.Debug("server: failed to send SOCKSReady failure", "err", werr)
			}
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

		// r=targetConn (internet target), w=targetConn; cw satisfies tunnel.CtrlWriter directly.
		_ = tunnel.RelayMuxChannel(ctx, targetConn, targetConn, ch, cw, sc.ConnID, protocol.MsgSOCKSClose, log)

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
	cw *tunnel.CtrlConnWriter,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestHTTPTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestHTTPTunnel", "id", client.ID, "err", err)
		if werr := cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Status: "error",
			Error:  "malformed RequestHTTPTunnel payload",
		}); werr != nil {
			log.Debug("failed to send HTTP tunnel error response", "id", client.ID, "err", werr)
		}
		return
	}
	handleHTTPTunnelRequest(false, req.Hostname, req.LocalHost, req.LocalPort, client, cw, mgr, dataAddr, log)
}

// handleRequestHTTPSTunnel processes a MsgRequestHTTPSTunnel from a client.
// It registers the SNI hostname in the TunnelManager's HTTPS routing table.
func handleRequestHTTPSTunnel(
	env *protocol.Envelope,
	client *Client,
	cw *tunnel.CtrlConnWriter,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestHTTPSTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestHTTPSTunnel", "id", client.ID, "err", err)
		if werr := cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Status: "error",
			Error:  "malformed RequestHTTPSTunnel payload",
		}); werr != nil {
			log.Debug("failed to send HTTPS tunnel error response", "id", client.ID, "err", werr)
		}
		return
	}
	handleHTTPTunnelRequest(true, req.Hostname, req.LocalHost, req.LocalPort, client, cw, mgr, dataAddr, log)
}

// handleHTTPTunnelRequest contains the shared logic for registering an HTTP or
// HTTPS tunnel and replying to the client.  isTLS=true selects the HTTPS path.
func handleHTTPTunnelRequest(
	isTLS bool,
	hostname, localHost string,
	localPort int,
	client *Client,
	cw *tunnel.CtrlConnWriter,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	// Validate and normalise the attacker-controlled fields. Lower-casing the
	// hostname makes the stored key match the lowercased SNI from peekSNI and
	// the lowercased HTTP Host lookup, eliminating a silent dead-tunnel mode.
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if err := protocol.ValidateDomain(hostname); err != nil {
		_ = cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Hostname: hostname, Status: "error", Error: "invalid hostname: " + err.Error(),
		})
		return
	}
	if err := protocol.ValidateTarget(localHost, localPort, 1); err != nil {
		_ = cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Hostname: hostname, Status: "error", Error: "invalid local target: " + err.Error(),
		})
		return
	}

	tunnelID := uuid.New().String()

	kind := "HTTP"
	var err error
	if isTLS {
		kind = "HTTPS"
		_, err = mgr.AddHTTPSTunnel(tunnelID, client.ID, hostname, localHost, localPort)
	} else {
		_, err = mgr.AddHTTPTunnel(tunnelID, client.ID, hostname, localHost, localPort)
	}
	if err != nil {
		// e.g. hostname already registered by a different client — tell the
		// loser instead of falsely replying "ok".
		log.Warn(kind+" tunnel registration rejected", "clientID", client.ID, "hostname", hostname, "err", err)
		_ = cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
			Hostname: hostname, Status: "error", Error: err.Error(),
		})
		return
	}

	log.Info(kind+" tunnel registered",
		"tunnelID", tunnelID,
		"clientID", client.ID,
		"hostname", hostname,
		"localHost", localHost,
		"localPort", localPort,
	)

	_ = cw.Write(protocol.MsgHTTPTunnelResp, protocol.HTTPTunnelResp{
		Hostname:       hostname,
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
	cw *tunnel.CtrlConnWriter,
	mgr *tunnel.Manager,
	dataAddr string,
	log *slog.Logger,
) {
	var req protocol.RequestTunnel
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&req); err != nil {
		log.Warn("failed to decode RequestTunnel", "id", client.ID, "err", err)
		if werr := cw.Write(protocol.MsgTunnelResp, protocol.TunnelResp{
			Status: "error",
			Error:  "malformed RequestTunnel payload",
		}); werr != nil {
			log.Debug("failed to send tunnel error response", "id", client.ID, "err", werr)
		}
		return
	}

	// Validate the attacker-controlled requested port and backend target.
	if req.RequestedPort != 0 {
		if err := protocol.ValidatePort(req.RequestedPort, 0); err != nil {
			_ = cw.Write(protocol.MsgTunnelResp, protocol.TunnelResp{
				Status: "error", Error: "invalid requested_port: " + err.Error(),
			})
			return
		}
	}
	if err := protocol.ValidateTarget(req.LocalHost, req.LocalPort, 1); err != nil {
		_ = cw.Write(protocol.MsgTunnelResp, protocol.TunnelResp{
			Status: "error", Error: "invalid local target: " + err.Error(),
		})
		return
	}

	// Choose the listen address.
	listenAddr := fmt.Sprintf(":%d", req.RequestedPort) // 0 → OS picks a port

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Error("failed to open public listener", "id", client.ID, "addr", listenAddr, "err", err)
		if werr := cw.Write(protocol.MsgTunnelResp, protocol.TunnelResp{
			Status: "error",
			Error:  fmt.Sprintf("could not listen on %s: %v", listenAddr, err),
		}); werr != nil {
			log.Debug("failed to send tunnel error response", "id", client.ID, "err", werr)
		}
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
	if err := cw.Write(protocol.MsgTunnelResp, protocol.TunnelResp{
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
	go tunnel.StartPublicListener(ctx, entry, cw, mgr, log)
}
