// Package socks — CLIENT-side SOCKS5 proxy listener.
//
// Reversed architecture:
//
//	SOCKS5 user (e.g. Claude) → Client:1080 → control tunnel → Server → Internet
//
// The client runs a SOCKS5 listener locally (e.g. localhost:1080).  For each
// CONNECT request it:
//  1. Completes the RFC 1928/1929 handshake.
//  2. Sends MsgSOCKSConnect to the server via the control connection.
//  3. Waits for MsgSOCKSReady (success/failure) from the server.
//  4. Relays raw bytes through MsgSOCKSData frames multiplexed over the single
//     control connection.  MsgSOCKSClose signals the end of a channel.
//
// This requires no separate data-port connectivity from the client to the
// server — all data travels through the pre-existing control TCP connection.
package socks

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// LastClientSOCKSAddr is the actual bound address after StartClientSOCKSProxy.
// Useful when addr uses ":0" so the OS assigns a free port.
var LastClientSOCKSAddr string

// ControlWriter is the interface used to write protocol messages to the
// server's control connection.  It must be safe for concurrent use.
type ControlWriter interface {
	WriteMsg(msgType protocol.MsgType, payload any) error
}

// StartClientSOCKSProxy starts the CLIENT-side SOCKS5 listener on addr.
//
//   - cw       — thread-safe writer for the control connection to the server.
//   - mux      — the SOCKSMux that the client message loop uses to dispatch
//     inbound MsgSOCKSData/MsgSOCKSClose/MsgSOCKSReady frames.
//   - authUser / authPass — enable RFC 1929 auth when both are non-empty.
func StartClientSOCKSProxy(
	ctx context.Context,
	addr string,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
	authUser, authPass string,
) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("socks5 client: listen %s: %w", addr, err)
	}

	LastClientSOCKSAddr = ln.Addr().String()
	log.Info("client SOCKS5 listener started", "addr", LastClientSOCKSAddr)

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
					log.Error("client SOCKS5 accept error", "err", err)
				}
				return
			}
			go handleClientSOCKSConn(ctx, conn, cw, mux, log, authUser, authPass)
		}
	}()

	return nil
}

// handleClientSOCKSConn handles one inbound SOCKS5 connection.
func handleClientSOCKSConn(
	ctx context.Context,
	conn net.Conn,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
	authUser, authPass string,
) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	targetHost, targetPort, err := NegotiateSOCKS5(conn, authUser, authPass, log)
	if err != nil {
		log.Debug("socks5 client: handshake failed", "err", err, "remote", conn.RemoteAddr())
		return
	}

	_ = conn.SetDeadline(time.Time{})

	log.Info("socks5 client: CONNECT request",
		"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
		"remote", conn.RemoteAddr(),
	)

	// ------------------------------------------------------------------ //
	// Phase 4 — Allocate mux channel and signal server
	// ------------------------------------------------------------------ //

	connID := uuid.New().String()

	// Allocate the channel BEFORE sending the message to avoid a race where
	// the server replies before we are listening on ReadyCh.
	ch, err := mux.NewChannel(connID)
	if err != nil {
		log.Warn("socks5 client: mux.NewChannel failed", "connID", connID, "err", err)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}
	defer mux.Remove(connID)

	if err := cw.WriteMsg(protocol.MsgSOCKSConnect, protocol.SOCKSConnect{
		ConnID:     connID,
		TargetHost: targetHost,
		TargetPort: targetPort,
	}); err != nil {
		log.Warn("socks5 client: failed to send SOCKSConnect", "connID", connID, "err", err)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 5 — Wait for server's ready signal
	// ------------------------------------------------------------------ //

	var ready tunnel.SOCKSReadyResult
	select {
	case ready = <-ch.ReadyCh:
	case <-time.After(30 * time.Second):
		log.Warn("socks5 client: timeout waiting for server dial", "connID", connID)
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	case <-ctx.Done():
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	case <-ch.Done():
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return
	}

	if !ready.Success {
		log.Warn("socks5 client: server dial failed",
			"connID", connID,
			"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
			"err", ready.ErrMsg,
		)
		sendSOCKSReply(conn, repConnRefused, nil, 0)
		return
	}

	// ------------------------------------------------------------------ //
	// Phase 6 — Success reply + bidirectional relay via mux
	// ------------------------------------------------------------------ //

	sendSOCKSReply(conn, repSuccess, net.IPv4zero, 0)

	log.Info("socks5 client: relay started",
		"connID", connID,
		"target", fmt.Sprintf("%s:%d", targetHost, targetPort),
	)

	// outSend carries payloads from the local SOCKS client to the mux writer.
	outSend := make(chan []byte, 64)
	muxWriterDone := make(chan struct{})

	// Goroutine A: local SOCKS client → server
	// Reads from conn, pumps payloads into outSend.
	// Closes outSend when the local conn reaches EOF / CloseWrite.
	go func() {
		defer close(outSend)
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				select {
				case outSend <- payload:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Goroutine B: server → local SOCKS client
	// Reads from ch.Recv (MsgSOCKSData from server), writes to conn.
	// Exits when ch.Recv returns EOF (recvW closed by mux.Remove / DeliverClose).
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = io.Copy(conn, ch.Recv)
	}()

	// Mux writer: drains outSend → MsgSOCKSData to server.
	// Exits after outSend is closed (goroutine A finished).
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

	// Half-close sequence:
	//   1. Wait until goroutine A has finished reading AND the mux writer has
	//      sent all data to the server.
	//   2. Send MsgSOCKSClose — tells the server we won't send any more data.
	//   3. The server will eventually echo back remaining data and then send
	//      its own MsgSOCKSClose.
	//   4. The client dispatcher receives MsgSOCKSClose → calls
	//      clientMux.DeliverClose(connID) → closes ch.recvW → goroutine B
	//      gets EOF from ch.Recv and exits.
	//   5. Wait for goroutine B.
	<-muxWriterDone
	_ = cw.WriteMsg(protocol.MsgSOCKSClose, protocol.SOCKSClose{ConnID: connID})

	// Wait for the server to finish sending (goroutine B exits on DeliverClose).
	<-recvDone

	// Final cleanup: remove the channel from the mux (idempotent if already
	// removed by DeliverClose).
	mux.Remove(connID)

	log.Info("socks5 client: relay finished", "connID", connID)
}
