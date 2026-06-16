package socks

import (
	"encoding/binary"
	"net"
)

// SOCKS5 wire constants (RFC 1928 / RFC 1929), shared by the client-side
// listener (client.go), the protocol negotiation (proto.go), and the HTTP
// CONNECT frontend.
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

// sendSOCKSReply writes a SOCKS5 reply with the given REP code. The bound
// address is always encoded in IPv4 format for simplicity.
func sendSOCKSReply(conn net.Conn, rep byte, boundAddr net.IP, boundPort int) {
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
