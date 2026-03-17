package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net"
)

// WriteMessage encodes payload using gob, wraps it in an Envelope, and writes
// the frame to conn with a 4-byte big-endian length prefix.
func WriteMessage(conn net.Conn, msgType MsgType, payload any) error {
	// Encode payload into bytes.
	var payloadBuf bytes.Buffer
	if err := gob.NewEncoder(&payloadBuf).Encode(payload); err != nil {
		return fmt.Errorf("framing: encode payload: %w", err)
	}

	// Build Envelope and encode it.
	env := Envelope{
		Type:    msgType,
		Payload: payloadBuf.Bytes(),
	}
	var envBuf bytes.Buffer
	if err := gob.NewEncoder(&envBuf).Encode(env); err != nil {
		return fmt.Errorf("framing: encode envelope: %w", err)
	}

	envBytes := envBuf.Bytes()
	length := uint32(len(envBytes))

	// Write 4-byte big-endian length prefix.
	if err := binary.Write(conn, binary.BigEndian, length); err != nil {
		return fmt.Errorf("framing: write length prefix: %w", err)
	}

	// Write envelope payload.
	if _, err := conn.Write(envBytes); err != nil {
		return fmt.Errorf("framing: write envelope: %w", err)
	}

	return nil
}

// ReadMessage reads a length-prefixed frame from conn and returns the decoded
// Envelope. Returns an error if the message exceeds MaxMessageSize, if the
// connection is closed, or if the data is truncated.
func ReadMessage(conn net.Conn) (*Envelope, error) {
	// Read 4-byte big-endian length prefix.
	var length uint32
	if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("framing: read length prefix: %w", err)
	}

	// DoS guard.
	if length > MaxMessageSize {
		return nil, fmt.Errorf("framing: message too large: %d bytes", length)
	}

	// Read exactly length bytes.
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, fmt.Errorf("framing: read envelope body: %w", err)
	}

	// Decode Envelope.
	var env Envelope
	if err := gob.NewDecoder(bytes.NewReader(buf)).Decode(&env); err != nil {
		return nil, fmt.Errorf("framing: decode envelope: %w", err)
	}

	return &env, nil
}
