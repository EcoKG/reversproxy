package socks_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared SOCKS5 test helpers (used by client_test.go and httpconnect_test.go).
// ---------------------------------------------------------------------------

// startEchoServer starts a local TCP echo server and returns its address. It is
// closed when ctx is cancelled.
func startEchoServer(t *testing.T, ctx context.Context) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}

	go func() {
		defer ln.Close()
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// dialSOCKS5NoAuth performs the SOCKS5 handshake with NO_AUTH and CONNECT
// to the given target host:port. Returns the connected net.Conn ready for data.
func dialSOCKS5NoAuth(t *testing.T, socksAddr, targetHost string, targetPort int) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5 server: %v", err)
	}

	// Greeting: VER=5, NMETHODS=1, METHOD=0 (NO_AUTH)
	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		conn.Close()
		t.Fatalf("write greeting: %v", err)
	}

	// Server choice
	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		conn.Close()
		t.Fatalf("read server choice: %v", err)
	}
	if choice[0] != 0x05 || choice[1] != 0x00 {
		conn.Close()
		t.Fatalf("unexpected server choice: %v", choice)
	}

	// CONNECT request
	if err := sendConnectRequest(conn, targetHost, targetPort); err != nil {
		conn.Close()
		t.Fatalf("send CONNECT: %v", err)
	}

	// Read reply
	if err := readConnectReply(conn); err != nil {
		conn.Close()
		t.Fatalf("read CONNECT reply: %v", err)
	}

	return conn
}

// dialSOCKS5Auth performs the full SOCKS5 + RFC 1929 auth handshake.
func dialSOCKS5Auth(t *testing.T, socksAddr, user, pass, targetHost string, targetPort int) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5 server: %v", err)
	}

	// Greeting: VER=5, NMETHODS=1, METHOD=2 (USER/PASS)
	_, err = conn.Write([]byte{0x05, 0x01, 0x02})
	if err != nil {
		conn.Close()
		t.Fatalf("write greeting: %v", err)
	}

	// Server choice
	choice := make([]byte, 2)
	if _, err := io.ReadFull(conn, choice); err != nil {
		conn.Close()
		t.Fatalf("read server choice: %v", err)
	}
	if choice[0] != 0x05 || choice[1] != 0x02 {
		conn.Close()
		t.Fatalf("unexpected server choice: want [5 2] got %v", choice)
	}

	// Auth sub-negotiation (RFC 1929)
	uBytes := []byte(user)
	pBytes := []byte(pass)
	authMsg := make([]byte, 0, 3+len(uBytes)+len(pBytes))
	authMsg = append(authMsg, 0x01)
	authMsg = append(authMsg, byte(len(uBytes)))
	authMsg = append(authMsg, uBytes...)
	authMsg = append(authMsg, byte(len(pBytes)))
	authMsg = append(authMsg, pBytes...)
	if _, err := conn.Write(authMsg); err != nil {
		conn.Close()
		t.Fatalf("write auth: %v", err)
	}

	// Auth response
	authResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, authResp); err != nil {
		conn.Close()
		t.Fatalf("read auth response: %v", err)
	}
	if authResp[1] != 0x00 {
		conn.Close()
		t.Fatalf("auth failed: status %d", authResp[1])
	}

	// CONNECT request
	if err := sendConnectRequest(conn, targetHost, targetPort); err != nil {
		conn.Close()
		t.Fatalf("send CONNECT: %v", err)
	}

	// Read reply
	if err := readConnectReply(conn); err != nil {
		conn.Close()
		t.Fatalf("read CONNECT reply: %v", err)
	}

	return conn
}

func sendConnectRequest(conn net.Conn, host string, port int) error {
	// Try to parse as IP first; otherwise use domain type.
	ip := net.ParseIP(host)
	var req []byte

	if ip4 := ip.To4(); ip4 != nil {
		req = make([]byte, 10)
		req[0] = 0x05
		req[1] = 0x01 // CONNECT
		req[2] = 0x00 // RSV
		req[3] = 0x01 // ATYP IPv4
		copy(req[4:8], ip4)
		binary.BigEndian.PutUint16(req[8:10], uint16(port))
	} else if ip6 := ip.To16(); ip6 != nil && ip != nil {
		req = make([]byte, 22)
		req[0] = 0x05
		req[1] = 0x01
		req[2] = 0x00
		req[3] = 0x04 // ATYP IPv6
		copy(req[4:20], ip6)
		binary.BigEndian.PutUint16(req[20:22], uint16(port))
	} else {
		// Domain name
		hBytes := []byte(host)
		req = make([]byte, 7+len(hBytes))
		req[0] = 0x05
		req[1] = 0x01
		req[2] = 0x00
		req[3] = 0x03 // ATYP domain
		req[4] = byte(len(hBytes))
		copy(req[5:], hBytes)
		binary.BigEndian.PutUint16(req[5+len(hBytes):], uint16(port))
	}

	_, err := conn.Write(req)
	return err
}

func readConnectReply(conn net.Conn) error {
	// VER + REP + RSV + ATYP = 4 bytes, then addr + port
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return fmt.Errorf("read reply header: %w", err)
	}
	if hdr[1] != 0x00 {
		return fmt.Errorf("CONNECT failed, REP=0x%02x", hdr[1])
	}

	// Consume remaining address bytes.
	switch hdr[3] {
	case 0x01: // IPv4
		tail := make([]byte, 4+2)
		_, _ = io.ReadFull(conn, tail)
	case 0x03: // domain
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			return err
		}
		tail := make([]byte, int(lb[0])+2)
		_, _ = io.ReadFull(conn, tail)
	case 0x04: // IPv6
		tail := make([]byte, 16+2)
		_, _ = io.ReadFull(conn, tail)
	}

	return nil
}
