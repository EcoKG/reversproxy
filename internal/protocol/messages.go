package protocol

// MsgType identifies the kind of message carried in an Envelope.
type MsgType uint8

const (
	MsgClientRegister MsgType = 1
	MsgRegisterResp   MsgType = 2
	MsgPing           MsgType = 3
	MsgPong           MsgType = 4
	MsgDisconnect     MsgType = 5
	MsgDisconnectAck  MsgType = 6

	// Tunnel management messages (Phase 2).
	MsgRequestTunnel  MsgType = 7  // client → server: request a public port for a local service
	MsgTunnelResp     MsgType = 8  // server → client: assigned public port or error
	MsgOpenConnection MsgType = 9  // server → client: a new external user has connected; open a data conn
	MsgDataConnHello  MsgType = 10 // client → server (data conn): identifies which connID this data conn handles
)

// MaxMessageSize is the maximum allowed byte length of a framed message.
// Messages exceeding this limit are rejected to prevent DoS via memory exhaustion.
const MaxMessageSize = 1 * 1024 * 1024 // 1 MB

// Envelope is the outer wrapper written on the wire.
// Type identifies which concrete message struct is gob-encoded in Payload.
type Envelope struct {
	Type    MsgType
	Payload []byte
}

// ClientRegister is sent by the client immediately after the TLS handshake
// to authenticate and identify itself to the server.
type ClientRegister struct {
	AuthToken string
	Name      string
	Version   string
}

// RegisterResp is the server's reply to a ClientRegister message.
// Status is "ok" on success and "error" on failure; Error carries the reason.
type RegisterResp struct {
	AssignedClientID string
	ServerID         string
	Status           string // "ok" | "error"
	Error            string
}

// Ping is sent by the server on a regular interval to verify that the client
// is still reachable. Seq allows the client to echo the same sequence number
// in its Pong reply.
type Ping struct {
	Seq uint64
}

// Pong is the client's reply to a Ping. Seq must match the Ping's Seq value.
type Pong struct {
	Seq uint64
}

// Disconnect is sent by either side to initiate a graceful shutdown of the
// control connection. Reason is a human-readable explanation.
type Disconnect struct {
	Reason string
}

// DisconnectAck is the acknowledgement of a Disconnect message.
// It carries no fields; receipt signals that the sender may close the conn.
type DisconnectAck struct{}

// RequestTunnel is sent by the client to ask the server to open a public TCP
// port and forward incoming connections to the client's local service at
// LocalHost:LocalPort.
type RequestTunnel struct {
	// LocalHost is the hostname or IP of the service behind the client (e.g. "127.0.0.1").
	LocalHost string
	// LocalPort is the port of the service behind the client (e.g. 8080).
	LocalPort int
	// RequestedPort is an optional hint for which public port to use (0 = any).
	RequestedPort int
}

// TunnelResp is the server's reply to a RequestTunnel message.
type TunnelResp struct {
	// TunnelID uniquely identifies this tunnel on the server.
	TunnelID string
	// PublicPort is the port that external users should connect to.
	PublicPort int
	// ServerDataAddr is the host:port clients must dial to open a data connection.
	ServerDataAddr string
	// Status is "ok" on success and "error" on failure.
	Status string
	// Error is a human-readable failure description (empty on success).
	Error string
}

// OpenConnection is sent by the server to the client whenever an external user
// connects to the public port. The client must dial the server's data address,
// send a DataConnHello with the same ConnID, and then relay data to the local
// service at LocalHost:LocalPort.
type OpenConnection struct {
	// TunnelID is the tunnel this connection belongs to.
	TunnelID string
	// ConnID uniquely identifies this particular external connection.
	ConnID string
	// LocalHost and LocalPort tell the client which local service to connect to.
	LocalHost string
	LocalPort int
}

// DataConnHello is the first message sent by the client on a new data
// connection to identify which external connection it is handling.
type DataConnHello struct {
	ConnID string
}
