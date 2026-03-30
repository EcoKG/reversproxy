// Package socks — CLIENT-side TCP port forwarder through the SOCKS tunnel.
//
// Listens on a local TCP port and forwards each connection to a remote
// host:port through the existing control-channel mux (MsgSOCKSConnect/Data/Close).
// This replaces the need for external tools like socat.
//
// Example: local :13389 → tunnel → server → 192.168.0.5:3389 (RDP)
package socks

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/EcoKG/reversproxy/internal/protocol"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

// StartPortForward listens on bindAddr:localPort and forwards each connection
// to remoteHost:remotePort through the tunnel mux.
func StartPortForward(
	ctx context.Context,
	localPort int,
	remoteHost string,
	remotePort int,
	bindAddr string,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) error {
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, localPort)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port forward: listen %s: %w", addr, err)
	}

	log.Info("port forward started",
		"listen", addr,
		"remote", fmt.Sprintf("%s:%d", remoteHost, remotePort),
	)

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
					log.Error("port forward: accept error", "err", err)
				}
				return
			}
			go handlePortForward(ctx, conn, remoteHost, remotePort, cw, mux, log)
		}
	}()

	return nil
}

func handlePortForward(
	ctx context.Context,
	conn net.Conn,
	remoteHost string,
	remotePort int,
	cw ControlWriter,
	mux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	defer conn.Close()

	connID := uuid.New().String()
	target := fmt.Sprintf("%s:%d", remoteHost, remotePort)

	log.Info("port forward: new connection", "connID", connID, "target", target, "from", conn.RemoteAddr())

	// Allocate mux channel before sending message
	ch, err := mux.NewChannel(connID)
	if err != nil {
		log.Warn("port forward: mux.NewChannel failed", "connID", connID, "err", err)
		return
	}
	defer mux.Remove(connID)

	// Request server to dial remote target
	if err := cw.WriteMsg(protocol.MsgSOCKSConnect, protocol.SOCKSConnect{
		ConnID:     connID,
		TargetHost: remoteHost,
		TargetPort: remotePort,
	}); err != nil {
		log.Warn("port forward: failed to send SOCKSConnect", "connID", connID, "err", err)
		return
	}

	// Wait for server ready
	var ready tunnel.SOCKSReadyResult
	select {
	case ready = <-ch.ReadyCh:
	case <-time.After(30 * time.Second):
		log.Warn("port forward: timeout", "connID", connID, "target", target)
		return
	case <-ctx.Done():
		return
	case <-ch.Done():
		return
	}

	if !ready.Success {
		log.Warn("port forward: server dial failed", "connID", connID, "target", target, "err", ready.ErrMsg)
		return
	}

	log.Info("port forward: relay started", "connID", connID, "target", target)

	_ = tunnel.RelayMuxChannel(ctx, conn, conn, ch, ctrlWriterAdapter{cw}, connID, protocol.MsgSOCKSClose, log)

	log.Info("port forward: relay finished", "connID", connID, "target", target)
}
