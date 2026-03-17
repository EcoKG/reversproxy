package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starlyn/reversproxy/internal/protocol"
)

// LastHTTPSAddr is set by StartHTTPSProxy to the actual bound address (useful
// when addr is ":0" and the OS picks a port).
var LastHTTPSAddr string

// StartHTTPSProxy starts an HTTPS SNI-routing listener on addr.
//
// For each incoming TLS connection it:
//  1. Peeks the TLS ClientHello to extract the SNI server name.
//  2. Looks up the matching HTTPS tunnel in mgr by SNI hostname.
//  3. Sends an OpenConnection to the client's control connection.
//  4. Waits for the client's data connection.
//  5. Replays the peeked ClientHello bytes and then relays the raw TCP stream.
//
// TLS is NOT terminated at the proxy; the raw encrypted bytes are forwarded
// to the client so TLS termination happens at the client's local service.
func StartHTTPSProxy(ctx context.Context, addr string, mgr *Manager, ctrlConns *ControlConnRegistry, dataAddr string, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("https proxy: listen %s: %w", addr, err)
	}

	LastHTTPSAddr = ln.Addr().String()
	log.Info("HTTPS proxy (SNI) listener started", "addr", LastHTTPSAddr)

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
					log.Error("HTTPS proxy accept error", "err", err)
				}
				return
			}
			go handleHTTPSConn(ctx, conn, mgr, ctrlConns, dataAddr, log)
		}
	}()

	return nil
}

// handleHTTPSConn handles a single inbound TLS connection.
// It peeks the SNI from the ClientHello, routes, and relays raw bytes.
func handleHTTPSConn(
	ctx context.Context,
	extConn net.Conn,
	mgr *Manager,
	ctrlConns *ControlConnRegistry,
	dataAddr string,
	log *slog.Logger,
) {
	_ = extConn.SetDeadline(time.Now().Add(10 * time.Second))

	// Read enough of the TLS record to extract the ClientHello SNI.
	// We store all peeked bytes so they can be replayed to the data conn.
	peeked, sni, err := peekSNI(extConn)
	if err != nil {
		log.Warn("HTTPS proxy: failed to peek SNI", "err", err, "remote", extConn.RemoteAddr())
		extConn.Close()
		return
	}

	_ = extConn.SetDeadline(time.Time{})

	// Strip port if present.
	host := sni
	if h, _, err2 := net.SplitHostPort(sni); err2 == nil {
		host = h
	}

	if host == "" {
		log.Warn("HTTPS proxy: no SNI in ClientHello", "remote", extConn.RemoteAddr())
		extConn.Close()
		return
	}

	entry, ok := mgr.GetHTTPSTunnel(host)
	if !ok {
		log.Warn("HTTPS proxy: no tunnel for SNI", "sni", host)
		extConn.Close()
		return
	}

	clientConn, ok := ctrlConns.Get(entry.ClientID)
	if !ok {
		log.Warn("HTTPS proxy: client not connected", "sni", host, "clientID", entry.ClientID)
		extConn.Close()
		return
	}

	connID := uuid.New().String()
	log.Info("HTTPS proxy: routing connection",
		"sni", host,
		"connID", connID,
		"clientID", entry.ClientID,
		"localAddr", fmt.Sprintf("%s:%d", entry.LocalHost, entry.LocalPort),
	)

	pending := mgr.RegisterPending(connID, extConn)

	openMsg := protocol.OpenConnection{
		TunnelID:  entry.ID,
		ConnID:    connID,
		LocalHost: entry.LocalHost,
		LocalPort: entry.LocalPort,
	}
	if err := protocol.WriteMessage(clientConn, protocol.MsgOpenConnection, openMsg); err != nil {
		log.Warn("HTTPS proxy: failed to send OpenConnection", "connID", connID, "err", err)
		extConn.Close()
		return
	}

	go relayHTTPSConn(ctx, pending, connID, peeked, log)
}

// relayHTTPSConn waits for the client data connection, replays the peeked
// TLS bytes, then relays bidirectionally at the raw TCP level.
func relayHTTPSConn(ctx context.Context, pending *pendingConn, connID string, peeked []byte, log *slog.Logger) {
	waitDone := make(chan net.Conn, 1)
	go func() {
		waitDone <- WaitReady(pending)
	}()

	var dataConn net.Conn
	select {
	case dataConn = <-waitDone:
	case <-time.After(15 * time.Second):
		log.Warn("HTTPS proxy: timeout waiting for data conn", "connID", connID)
		PendingExtConn(pending).Close()
		return
	case <-ctx.Done():
		PendingExtConn(pending).Close()
		return
	}

	extConn := PendingExtConn(pending)

	// Replay the peeked TLS bytes into the data connection so the client's
	// local TLS service receives the complete ClientHello.
	if len(peeked) > 0 {
		if _, err := dataConn.Write(peeked); err != nil {
			log.Warn("HTTPS proxy: failed to replay peeked bytes", "connID", connID, "err", err)
			extConn.Close()
			dataConn.Close()
			return
		}
	}

	log.Info("HTTPS proxy: relay started", "connID", connID)

	done := make(chan struct{}, 2)

	go func() {
		copyAndClose(dataConn, extConn)
		done <- struct{}{}
	}()

	go func() {
		copyAndClose(extConn, dataConn)
		done <- struct{}{}
	}()

	<-done
	<-done

	extConn.Close()
	dataConn.Close()

	log.Info("HTTPS proxy: relay finished", "connID", connID)
}

// peekSNI reads the first TLS record from r, parses a ClientHello, and
// returns all bytes read together with the SNI server name.
// If the record is not a ClientHello (e.g. not TLS) an empty SNI is returned
// with no error so the caller can decide what to do.
func peekSNI(r io.Reader) (peeked []byte, sni string, err error) {
	// TLS record header: 1 byte content-type + 2 bytes version + 2 bytes length.
	const tlsHeaderLen = 5
	hdr := make([]byte, tlsHeaderLen)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return nil, "", fmt.Errorf("read TLS header: %w", err)
	}

	peeked = append(peeked, hdr...)

	// Content type 0x16 = Handshake.
	if hdr[0] != 0x16 {
		return peeked, "", nil // not a TLS handshake — no SNI
	}

	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen <= 0 || recLen > 16384 {
		return peeked, "", fmt.Errorf("invalid TLS record length: %d", recLen)
	}

	body := make([]byte, recLen)
	if _, err = io.ReadFull(r, body); err != nil {
		return peeked, "", fmt.Errorf("read TLS record body: %w", err)
	}
	peeked = append(peeked, body...)

	sni = parseSNIFromClientHello(body)
	return peeked, sni, nil
}

// parseSNIFromClientHello parses the SNI server_name extension from the
// body of a TLS ClientHello record (without the 5-byte record header).
// Returns an empty string if the extension is not present or parsing fails.
func parseSNIFromClientHello(data []byte) string {
	// Handshake message layout inside the record body:
	//   1 byte  handshake type (0x01 = ClientHello)
	//   3 bytes length
	//   2 bytes client_version
	//   32 bytes random
	//   1 byte session_id_length + session_id
	//   2 bytes cipher_suites_length + cipher_suites
	//   1 byte compression_methods_length + compression_methods
	//   2 bytes extensions_length + extensions

	if len(data) < 4 {
		return ""
	}
	if data[0] != 0x01 { // ClientHello
		return ""
	}

	// Skip: type(1) + length(3) + version(2) + random(32) = 38 bytes minimum
	pos := 4
	if len(data) < pos+2+32 {
		return ""
	}
	pos += 2 + 32 // skip version + random

	// Session ID
	if len(data) < pos+1 {
		return ""
	}
	sessIDLen := int(data[pos])
	pos++
	pos += sessIDLen

	// Cipher suites
	if len(data) < pos+2 {
		return ""
	}
	cipherLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherLen

	// Compression methods
	if len(data) < pos+1 {
		return ""
	}
	compLen := int(data[pos])
	pos++
	pos += compLen

	// Extensions length
	if len(data) < pos+2 {
		return ""
	}
	extLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2

	end := pos + extLen
	if len(data) < end {
		end = len(data)
	}

	// Walk extensions.
	for pos+4 <= end {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extDataLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4

		if pos+extDataLen > end {
			break
		}

		if extType == 0x0000 { // server_name extension
			return parseSNIExtension(data[pos : pos+extDataLen])
		}

		pos += extDataLen
	}

	return ""
}

// parseSNIExtension extracts the first host_name entry from the server_name
// extension value bytes.
func parseSNIExtension(data []byte) string {
	// server_name_list length (2 bytes)
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	pos := 2
	end := pos + listLen
	if len(data) < end {
		end = len(data)
	}

	for pos+3 <= end {
		nameType := data[pos]
		nameLen := int(binary.BigEndian.Uint16(data[pos+1 : pos+3]))
		pos += 3
		if pos+nameLen > end {
			break
		}
		if nameType == 0x00 { // host_name
			return strings.ToLower(string(data[pos : pos+nameLen]))
		}
		pos += nameLen
	}

	return ""
}

// copyAndClose copies from src to dst, then half-closes dst if it is a TCPConn.
func copyAndClose(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}
