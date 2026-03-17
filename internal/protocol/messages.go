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
