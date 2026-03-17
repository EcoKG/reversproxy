package tunnel

import (
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

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

	// 2. Send DataConnHello.
	if err := protocol.WriteMessage(dataConn, protocol.MsgDataConnHello, protocol.DataConnHello{
		ConnID: msg.ConnID,
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

	log.Info("relay started (client side)", "localAddr", localAddr)

	// 4. Bidirectional relay.
	done := make(chan struct{}, 2)

	go func() {
		_, err := io.Copy(localConn, dataConn)
		if err != nil && !isClosedErr(err) {
			log.Debug("relay server→local done", "err", err)
		}
		if tc, ok := localConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(dataConn, localConn)
		if err != nil && !isClosedErr(err) {
			log.Debug("relay local→server done", "err", err)
		}
		if tc, ok := dataConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	log.Info("relay finished (client side)")
	return nil
}
