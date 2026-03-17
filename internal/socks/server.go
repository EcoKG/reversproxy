// Package socks implements a SOCKS5 proxy listener (RFC 1928 / RFC 1929).
//
// Architecture:
//
//	Browser (SOCKS5) → Server:1080 → control channel → Client → Internet
//
// The server performs the SOCKS5 handshake and extracts the target host:port.
// It then signals the connected client (via the control channel) to dial that
// target. The client performs DNS resolution and dials, then opens a data
// connection back to the server. Finally, the server relays raw TCP data
// bidirectionally between the SOCKS5 client and the tunnel data connection.
package socks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// LastSOCKSAddr is set by StartSOCKSProxy to the actual bound address.
// Useful when addr is ":0" and the OS assigns a port.
var LastSOCKSAddr string

// SOCKS5 protocol constants (RFC 1928).
const (
	socks5Version = 0x05

	authNone     = 0x00 // no authentication required
	authPassword = 0x02 // username/password (RFC 1929)
	authNoAccept = 0xFF // no acceptable methods

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess          = 0x00
	repGeneralFailure   = 0x01
	repConnRefused      = 0x05
	repCmdNotSupported  = 0x07
	repAddrNotSupported = 0x08
)

// StartSOCKSProxy starts a SOCKS5 proxy listener on addr.
//
// Connections are handled by signalling the first available client (selected
// by PickAny on ctrlConns) to dial the requested target on behalf of the
// SOCKS5 user.
//
// authUser and authPass control optional username/password authentication
// (RFC 1929). Both must be non-empty to enable auth; if either is empty,
// the listener accepts connections without authentication.
func StartSOCKSProxy(
	ctx context.Context,
	addr string,
	mgr *tunnel.Manager,
	ctrlConns *tunnel.ControlConnRegistry,
	dataAddr string,
	log *slog.Logger,
	authUser, authPass string,
) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("socks5: listen %s: %w", addr, err)
	}

	LastSOCKSAddr = ln.Addr().String()
	log.Info("SOCKS5 proxy listener started", "addr", LastSOCKSAddr)

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
					log.Error("SOCKS5 accept error", "err", err)
				}
				return
			}
			go handleSOCKSConn(ctx, conn, mgr, ctrlConns, dataAddr, log, authUser, authPass)
		}
	}()

	return nil
}

// handleSOCKSConn is the per-connection SOCKS5 handler.
func handleSOCKSConn(
	ctx context.Context,
	conn net.Conn,
	mgr *tunnel.Manager,
	ctrlConns *tunnel.ControlConnRegistry,
	dataAddr string,
	log *slog.Logger,
	authUser, authPass string,
) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// ------------------------------------------------------------------ //
	// Phase 1 — Client greeting / method negotiation
	// ------------------------------------------------------------------ //

	// Read VER + NMETHODS
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		log.Debug("socks5: failed to read greeting header", "err", err)
		return
	}
	if hdr[0] != socks5Version {
		log.Debug("socks5: unsupported version", "ver", hdr[0])
		return
	}

	nMethods := int(hdr[1])
	if nMethods == 0 {
		_, _ = conn.Write([]byte{socks5Version, authNoAccept})
		return
	}

	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		log.Debug("socks5: failed to read methods", "err", err)
		return
	}

	// Choose authentication method.
	authRequired := authUser != "" && authPass != ""

	selectedMethod := byte(authNoAccept)
	for _, m := range methods {
		if authRequired && m == authPassword {
			selectedMethod = authPassword
			break
		}
		if !authRequired && m == authNone {
			selectedMethod = authNone
			break
		}
	}

	if selectedMethod == authNoAccept {
		_, _ = conn.Write([]byte{socks5Version, authNoAccept})
		return
	}

	// Send chosen method.
	if _, err := conn.Write([]byte{socks5Version, selectedMethod}); err != nil {
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 2 — Authentication sub-negotiation (RFC 1929)
	// ------------------------------------------------------------------ //

	if selectedMethod == authPassword {
		// Sub-negotiation: [VER=0x01, ULEN, UNAME..., PLEN, PASSWD...]
		authHdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHdr); err != nil {
			return
		}
		if authHdr[0] != 0x01 {
			// Invalid sub-negotiation version.
			_, _ = conn.Write([]byte{0x01, 0x01}) // failure
			return
		}

		uLen := int(authHdr[1])
		uBuf := make([]byte, uLen)
		if _, err := io.ReadFull(conn, uBuf); err != nil {
			return
		}

		pLenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, pLenBuf); err != nil {
			return
		}

		pBuf := make([]byte, int(pLenBuf[0]))
		if _, err := io.ReadFull(conn, pBuf); err != nil {
			return
		}

		if string(uBuf) != authUser || string(pBuf) != authPass {
			_, _ = conn.Write([]byte{0x01, 0x01}) // auth failure
			log.Warn("socks5: authentication failed", "remote", conn.RemoteAddr())
			return
		}

		// Auth success.
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
	}

	// ------------------------------------------------------------------ //
	// Phase 3 — CONNECT request
	// ------------------------------------------------------------------ //

	// [VER, CMD, RSV, ATYP, DST.ADDR..., DST.PORT(2)]
	reqHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHdr); err != nil {
		log.Debug("socks5: failed to read request header", "err", err)
		return
	}

	if reqHdr[0] != socks5Version {
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	if reqHdr[1] != cmdConnect {
		sendSOCKSReply(conn, repCmdNotSupported, nil, 0)
		return
	}

	// Parse destination address.
	atyp := reqHdr[3]
	var targetHost string

	switch atyp {
	case atypIPv4:
		addr4 := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr4); err != nil {
			return
		}
		targetHost = net.IP(addr4).String()

	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		domainBuf := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, domainBuf); err != nil {
			return
		}
		targetHost = string(domainBuf)

	case atypIPv6:
		addr6 := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr6); err != nil {
			return
		}
		targetHost = net.IP(addr6).String()

	default:
		sendSOCKSReply(conn, repAddrNotSupported, nil, 0)
		return
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return
	}
	targetPort := int(binary.BigEndian.Uint16(portBuf))

	// Remove the I/O deadline now that we have the full request.
	_ = conn.SetDeadline(time.Time{})

	log.Info("socks5: CONNECT request",
		"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
		"remote", conn.RemoteAddr(),
	)

	// ------------------------------------------------------------------ //
	// Phase 4 — Route via tunnel client
	// ------------------------------------------------------------------ //

	// Pick any available client control connection.
	clientConn, ok := ctrlConns.PickAny()
	if !ok {
		log.Warn("socks5: no client connected", "target", targetHost)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	connID := uuid.New().String()

	// Register both pending slots before sending the message to avoid
	// races where the client responds before we register.
	pendingSocks := mgr.RegisterPendingSOCKS(connID)
	pendingData := mgr.RegisterPending(connID, conn) // extConn for the relay

	// Send SOCKSConnect to client via control channel.
	msg := protocol.SOCKSConnect{
		ConnID:     connID,
		TargetHost: targetHost,
		TargetPort: targetPort,
	}
	if err := protocol.WriteMessage(clientConn, protocol.MsgSOCKSConnect, msg); err != nil {
		log.Warn("socks5: failed to send SOCKSConnect to client", "connID", connID, "err", err)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 5 — Wait for client's ready signal
	// ------------------------------------------------------------------ //

	type readyResult struct {
		ok     bool
		errMsg string
	}
	readyCh := make(chan readyResult, 1)

	go func() {
		ok, errMsg := tunnel.WaitSOCKSReady(pendingSocks)
		readyCh <- readyResult{ok, errMsg}
	}()

	var ready readyResult
	select {
	case ready = <-readyCh:
	case <-time.After(30 * time.Second):
		log.Warn("socks5: timeout waiting for client dial", "connID", connID)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	case <-ctx.Done():
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	if !ready.ok {
		log.Warn("socks5: client dial failed",
			"connID", connID,
			"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
			"err", ready.errMsg,
		)
		sendSOCKSReply(conn, repConnRefused, nil, 0)
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 6 — Wait for data connection (client dials back after success)
	// ------------------------------------------------------------------ //

	dataCh := make(chan net.Conn, 1)
	go func() {
		dataCh <- tunnel.WaitReady(pendingData)
	}()

	var dataConn net.Conn
	select {
	case dataConn = <-dataCh:
	case <-time.After(15 * time.Second):
		log.Warn("socks5: timeout waiting for data conn", "connID", connID)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	case <-ctx.Done():
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 7 — Send success reply, then relay
	// ------------------------------------------------------------------ //

	// Reply with bound address 0.0.0.0:0 (we don't expose the client's real address).
	sendSOCKSReply(conn, repSuccess, net.IPv4zero, 0)

	log.Info("socks5: relay started",
		"connID", connID,
		"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
	)

	defer dataConn.Close()

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(dataConn, conn)
		if tc, ok := dataConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(conn, dataConn)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	log.Info("socks5: relay finished", "connID", connID)
}

// sendSOCKSReply writes a SOCKS5 reply to conn.
// boundAddr and boundPort represent the BND.ADDR and BND.PORT fields;
// use nil/0 when not meaningful (e.g., on error).
func sendSOCKSReply(conn net.Conn, rep byte, boundAddr net.IP, boundPort int) {
	// Always reply with IPv4 format for simplicity.
	addr := boundAddr
	if len(addr) == 0 {
		addr = net.IPv4zero
	}
	if ip4 := addr.To4(); ip4 != nil {
		addr = ip4
	}

	reply := make([]byte, 10)
	reply[0] = socks5Version
	reply[1] = rep
	reply[2] = 0x00 // RSV
	reply[3] = atypIPv4
	copy(reply[4:8], addr[:4])
	binary.BigEndian.PutUint16(reply[8:10], uint16(boundPort))

	_, _ = conn.Write(reply)
}
