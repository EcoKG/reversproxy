package config

import "time"

// Network timeouts and intervals.
const (
	// HandshakeTimeout is the deadline for completing a registration handshake.
	HandshakeTimeout = 10 * time.Second

	// MessageReadTimeout is the read deadline applied in control message loops.
	MessageReadTimeout = 45 * time.Second

	// HeartbeatInterval is the period between Ping messages.
	HeartbeatInterval = 10 * time.Second

	// HeartbeatStaleThreshold is how long since the last Pong before a client
	// is considered stale.
	HeartbeatStaleThreshold = 30 * time.Second

	// PongTimeout is the maximum time to wait for a Pong response.
	PongTimeout = 10 * time.Second

	// DataConnWaitTimeout is how long proxy handlers wait for a client's data
	// connection to arrive before giving up.
	DataConnWaitTimeout = 15 * time.Second

	// SOCKSDialTimeout is how long the server waits for a SOCKS target dial
	// to complete.
	SOCKSDialTimeout = 15 * time.Second

	// SOCKSReadyTimeout is how long the SOCKS5 handler waits for the remote
	// side to report dial success/failure.
	SOCKSReadyTimeout = 30 * time.Second

	// TCPKeepAlivePeriod is the keepalive interval set on TCP connections.
	TCPKeepAlivePeriod = 15 * time.Second

	// ProxyReadTimeout is the deadline for reading the initial request
	// (HTTP Host / TLS ClientHello) on proxy listeners.
	ProxyReadTimeout = 10 * time.Second

	// SOCKSHandshakeTimeout is the deadline for completing a SOCKS5 handshake.
	SOCKSHandshakeTimeout = 30 * time.Second
)

// Buffer sizes.
const (
	// RelayBufSize is the buffer size used for bidirectional TCP relays.
	RelayBufSize = 32 * 1024

	// MuxChannelBuffer is the capacity of the outbound payload channel in
	// mux-based SOCKS relays.
	MuxChannelBuffer = 64
)
