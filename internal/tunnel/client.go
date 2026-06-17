package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

// HandleSOCKSConnect is called by the client message loop when a MsgSOCKSConnect
// message arrives from the server. It:
//  1. Dials the target host:port (DNS resolution happens here, on the client side).
//  2. Opens a data connection back to the server and sends DataConnHello.
//  3. Sends MsgSOCKSReady back on the control connection indicating success/failure.
//  4. If successful, relays data bidirectionally between the data conn and the target.
//
// All network work is done in a goroutine; the function returns immediately.
func HandleSOCKSConnect(
	msg protocol.SOCKSConnect,
	ctrlConn net.Conn,
	serverDataAddr string,
	log *slog.Logger,
) {
	log = log.With("connID", msg.ConnID, "target", fmt.Sprintf("%s:%d", msg.TargetHost, msg.TargetPort))

	go func() {
		if err := handleSOCKSConnectAsync(msg, ctrlConn, serverDataAddr, log); err != nil {
			log.Error("SOCKS connect handler failed", "err", err)
		}
	}()
}

func handleSOCKSConnectAsync(
	msg protocol.SOCKSConnect,
	ctrlConn net.Conn,
	serverDataAddr string,
	log *slog.Logger,
) error {
	targetAddr := fmt.Sprintf("%s:%d", msg.TargetHost, msg.TargetPort)

	// 1. Dial the target (DNS resolution happens here on the client side).
	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Warn("SOCKS: failed to dial target", "target", targetAddr, "err", err)
		if werr := protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  msg.ConnID,
			Success: false,
			Error:   err.Error(),
		}); werr != nil {
			log.Debug("failed to send SOCKSReady failure", "err", werr)
		}
		return nil // not a fatal error for the goroutine
	}
	defer targetConn.Close()
	if tc, ok := targetConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// 2. Open a data connection back to the server.
	dataConn, err := net.Dial("tcp", serverDataAddr)
	if err != nil {
		if werr := protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  msg.ConnID,
			Success: false,
			Error:   fmt.Sprintf("dial server data addr: %v", err),
		}); werr != nil {
			log.Debug("failed to send SOCKSReady failure", "err", werr)
		}
		return nil
	}
	defer dataConn.Close()
	if tc, ok := dataConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// 3. Identify this data connection to the server (echo the DataToken).
	if err := protocol.WriteMessage(dataConn, protocol.MsgDataConnHello, protocol.DataConnHello{
		ConnID: msg.ConnID,
		Token:  msg.DataToken,
	}); err != nil {
		if werr := protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
			ConnID:  msg.ConnID,
			Success: false,
			Error:   fmt.Sprintf("send DataConnHello: %v", err),
		}); werr != nil {
			log.Debug("failed to send SOCKSReady failure", "err", werr)
		}
		return nil
	}

	// 4. Tell the server the dial succeeded.
	if err := protocol.WriteMessage(ctrlConn, protocol.MsgSOCKSReady, protocol.SOCKSReady{
		ConnID:  msg.ConnID,
		Success: true,
	}); err != nil {
		return fmt.Errorf("send SOCKSReady: %w", err)
	}

	log.Info("SOCKS relay started (client side)", "target", targetAddr)

	// 5. Bidirectional relay between target and server data connection.
	RelayBiDirectional(context.Background(), targetConn, dataConn)

	log.Info("SOCKS relay finished (client side)")
	return nil
}

// HandleOpenConnection is called by the client message loop when an
// OpenConnection message arrives from the server. It:
//  1. Dials the server's data address.
//  2. Sends a DataConnHello to identify which external connection this is.
//  3. Dials the local service at localHost:localPort.
//  4. Relays data bidirectionally between the server data conn and the local service.
//
// The function returns immediately; all work happens in goroutines.
func HandleOpenConnection(
	msg protocol.OpenConnection,
	serverDataAddr string,
	log *slog.Logger,
) {
	log = log.With("connID", msg.ConnID, "tunnelID", msg.TunnelID)

	go func() {
		if err := handleOpenConnAsync(msg, serverDataAddr, log); err != nil {
			log.Error("open connection handler failed", "err", err)
		}
	}()
}

func handleOpenConnAsync(msg protocol.OpenConnection, serverDataAddr string, log *slog.Logger) error {
	// 1. Dial the server data port.
	dataConn, err := net.Dial("tcp", serverDataAddr)
	if err != nil {
		return fmt.Errorf("dial server data addr %q: %w", serverDataAddr, err)
	}
	defer func() {
		dataConn.Close()
	}()
	if tc, ok := dataConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// 2. Send DataConnHello (echo the single-use DataToken to prove ownership).
	if err := protocol.WriteMessage(dataConn, protocol.MsgDataConnHello, protocol.DataConnHello{
		ConnID: msg.ConnID,
		Token:  msg.DataToken,
	}); err != nil {
		return fmt.Errorf("write DataConnHello: %w", err)
	}

	// 3. Dial the local service.
	localAddr := fmt.Sprintf("%s:%d", msg.LocalHost, msg.LocalPort)
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		// We still own dataConn; close it so the server side cleans up.
		return fmt.Errorf("dial local service %q: %w", localAddr, err)
	}
	defer localConn.Close()
	if tc, ok := localConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	log.Info("relay started (client side)", "localAddr", localAddr)

	// 4. Bidirectional relay.
	RelayBiDirectional(context.Background(), localConn, dataConn)

	log.Info("relay finished (client side)")
	return nil
}
