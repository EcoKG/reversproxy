package socks

import (
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

// NegotiateSOCKS5 performs the SOCKS5 greeting, optional RFC 1929
// username/password authentication, and CONNECT request parsing.
//
// It returns the parsed target host and port on success. The caller is
// responsible for sending the SOCKS5 reply (success or failure) because
// client-side and server-side handlers write replies differently.
//
// On any protocol error it returns a non-nil error; the caller should send
// the appropriate SOCKS5 error reply and close the connection.
func NegotiateSOCKS5(conn net.Conn, authUser, authPass string, log *slog.Logger) (targetHost string, targetPort int, err error) {
	// ------------------------------------------------------------------ //
	// Phase 1 — Greeting / method negotiation
	// ------------------------------------------------------------------ //

	hdr := make([]byte, 2)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return "", 0, fmt.Errorf("read greeting header: %w", err)
	}
	if hdr[0] != socks5Version {
		return "", 0, fmt.Errorf("unsupported SOCKS version %d", hdr[0])
	}

	nMethods := int(hdr[1])
	if nMethods == 0 {
		_, _ = conn.Write([]byte{socks5Version, authNoAccept})
		return "", 0, fmt.Errorf("client offered zero auth methods")
	}

	methods := make([]byte, nMethods)
	if _, err = io.ReadFull(conn, methods); err != nil {
		return "", 0, fmt.Errorf("read methods: %w", err)
	}

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
		return "", 0, fmt.Errorf("no acceptable auth method")
	}
	if _, err = conn.Write([]byte{socks5Version, selectedMethod}); err != nil {
		return "", 0, fmt.Errorf("write method selection: %w", err)
	}

	// ------------------------------------------------------------------ //
	// Phase 2 — RFC 1929 auth sub-negotiation
	// ------------------------------------------------------------------ //

	if selectedMethod == authPassword {
		authHdr := make([]byte, 2)
		if _, err = io.ReadFull(conn, authHdr); err != nil {
			return "", 0, fmt.Errorf("read auth header: %w", err)
		}
		if authHdr[0] != 0x01 {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return "", 0, fmt.Errorf("invalid auth sub-negotiation version %d", authHdr[0])
		}

		uBuf := make([]byte, int(authHdr[1]))
		if _, err = io.ReadFull(conn, uBuf); err != nil {
			return "", 0, fmt.Errorf("read auth username: %w", err)
		}

		pLenBuf := make([]byte, 1)
		if _, err = io.ReadFull(conn, pLenBuf); err != nil {
			return "", 0, fmt.Errorf("read auth password length: %w", err)
		}

		pBuf := make([]byte, int(pLenBuf[0]))
		if _, err = io.ReadFull(conn, pBuf); err != nil {
			return "", 0, fmt.Errorf("read auth password: %w", err)
		}

		if subtle.ConstantTimeCompare(uBuf, []byte(authUser)) != 1 ||
			subtle.ConstantTimeCompare(pBuf, []byte(authPass)) != 1 {
			_, _ = conn.Write([]byte{0x01, 0x01})
			log.Warn("socks5: authentication failed", "remote", conn.RemoteAddr())
			return "", 0, fmt.Errorf("authentication failed")
		}

		if _, err = conn.Write([]byte{0x01, 0x00}); err != nil {
			return "", 0, fmt.Errorf("write auth success: %w", err)
		}
	}

	// ------------------------------------------------------------------ //
	// Phase 3 — CONNECT request
	// ------------------------------------------------------------------ //

	reqHdr := make([]byte, 4)
	if _, err = io.ReadFull(conn, reqHdr); err != nil {
		return "", 0, fmt.Errorf("read request header: %w", err)
	}
	if reqHdr[0] != socks5Version {
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return "", 0, fmt.Errorf("invalid request SOCKS version %d", reqHdr[0])
	}
	if reqHdr[1] != cmdConnect {
		sendSOCKSReply(conn, repCmdNotSupported, nil, 0)
		return "", 0, fmt.Errorf("unsupported command %d", reqHdr[1])
	}

	atyp := reqHdr[3]
	switch atyp {
	case atypIPv4:
		addr4 := make([]byte, 4)
		if _, err = io.ReadFull(conn, addr4); err != nil {
			return "", 0, fmt.Errorf("read IPv4 address: %w", err)
		}
		targetHost = net.IP(addr4).String()

	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err = io.ReadFull(conn, lenBuf); err != nil {
			return "", 0, fmt.Errorf("read domain length: %w", err)
		}
		domainBuf := make([]byte, int(lenBuf[0]))
		if _, err = io.ReadFull(conn, domainBuf); err != nil {
			return "", 0, fmt.Errorf("read domain: %w", err)
		}
		targetHost = string(domainBuf)

	case atypIPv6:
		addr6 := make([]byte, 16)
		if _, err = io.ReadFull(conn, addr6); err != nil {
			return "", 0, fmt.Errorf("read IPv6 address: %w", err)
		}
		targetHost = net.IP(addr6).String()

	default:
		sendSOCKSReply(conn, repAddrNotSupported, nil, 0)
		return "", 0, fmt.Errorf("unsupported address type %d", atyp)
	}

	portBuf := make([]byte, 2)
	if _, err = io.ReadFull(conn, portBuf); err != nil {
		return "", 0, fmt.Errorf("read port: %w", err)
	}
	targetPort = int(binary.BigEndian.Uint16(portBuf))

	if err = protocol.ValidateTarget(targetHost, targetPort, 1); err != nil {
		sendSOCKSReply(conn, repGeneralFailure, nil, 0)
		return "", 0, fmt.Errorf("invalid target: %w", err)
	}

	return targetHost, targetPort, nil
}
