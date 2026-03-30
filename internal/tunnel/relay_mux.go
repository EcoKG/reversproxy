package tunnel

import (
	"context"
	"io"
	"log/slog"
	"net"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

// CtrlWriter serializes protocol message writes over the control connection.
// It is safe for concurrent use.
type CtrlWriter interface {
	Write(msgType protocol.MsgType, payload any) error
}

// RelayMuxChannel relays data between a local connection and a SOCKSMux channel
// over the control plane.  It replaces the 4x-duplicated goroutine pattern in
// socks/client.go, socks/httpconnect.go, socks/portforward.go, and
// control/handler.go.
//
// Parameters:
//
//	ctx       — parent context (currently used for future cancellation hooks).
//	r         — source of outbound data (conn itself, or a bufio.Reader wrapping it
//	            when the caller has pre-buffered bytes during header parsing).
//	w         — destination for inbound data from the mux channel (always the local
//	            net.Conn so the peer can observe a closed write side).
//	ch        — the SOCKSChannel allocated in the mux for this connection.
//	cw        — control-plane writer for MsgSOCKSData / closeMsg frames.
//	connID    — echoed in every SOCKSData/SOCKSClose frame.
//	closeMsg  — the message type sent after the outbound half-close (MsgSOCKSClose).
//	log       — structured logger.
func RelayMuxChannel(
	ctx context.Context,
	r io.Reader,
	w net.Conn,
	ch *SOCKSChannel,
	cw CtrlWriter,
	connID string,
	closeMsg protocol.MsgType,
	log *slog.Logger,
) error {
	// outSend carries payloads from the local reader to the mux writer.
	outSend := make(chan []byte, 64)
	muxWriterDone := make(chan struct{})

	// Goroutine A: local reader → peer via MsgSOCKSData.
	// Reads raw bytes from r; pushes slices into outSend.
	// Closes outSend when r reaches EOF / error.
	go func() {
		defer close(outSend)
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
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

	// Goroutine B: mux channel → local writer (MsgSOCKSData from peer).
	// Reads from ch.Recv; writes to w.
	// Exits when ch.Recv returns EOF (pipe closed by mux.Remove / DeliverClose).
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = io.Copy(w, ch.Recv)
	}()

	// Mux writer: drains outSend → MsgSOCKSData frames to peer.
	// Exits after outSend is closed (goroutine A finished reading).
	go func() {
		defer close(muxWriterDone)
		for payload := range outSend {
			if err := cw.Write(protocol.MsgSOCKSData, protocol.SOCKSData{
				ConnID:  connID,
				Payload: payload,
			}); err != nil {
				// Drain outSend to unblock goroutine A.
				for range outSend {
				}
				return
			}
		}
	}()

	// Half-close sequence:
	//  1. Wait until goroutine A has finished AND the mux writer has sent all data.
	//  2. Send closeMsg (MsgSOCKSClose) — tells the peer we won't send any more data.
	//  3. The peer echoes remaining data and then sends its own MsgSOCKSClose, which
	//     causes mux.DeliverClose → pipe closed → goroutine B gets EOF and exits.
	//  4. Wait for goroutine B.
	<-muxWriterDone
	_ = cw.Write(closeMsg, protocol.SOCKSClose{ConnID: connID})

	<-recvDone

	log.Debug("relay_mux: relay finished", "connID", connID)
	return nil
}
