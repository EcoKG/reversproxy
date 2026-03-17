package protocol_test

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"io"
	"net"
	"testing"

	"github.com/EcoKG/reversproxy/internal/protocol"
)

// TestFramingRoundtrip verifies that WriteMessage followed by ReadMessage
// faithfully reconstructs the original message.
func TestFramingRoundtrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	original := protocol.ClientRegister{
		AuthToken: "secret-token",
		Name:      "test-client",
		Version:   "0.1.0",
	}

	// Write in a goroutine to avoid blocking on the synchronous pipe.
	errCh := make(chan error, 1)
	go func() {
		errCh <- protocol.WriteMessage(client, protocol.MsgClientRegister, original)
	}()

	env, err := protocol.ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage returned unexpected error: %v", err)
	}
	if writeErr := <-errCh; writeErr != nil {
		t.Fatalf("WriteMessage returned unexpected error: %v", writeErr)
	}

	if env.Type != protocol.MsgClientRegister {
		t.Errorf("envelope type = %v; want %v", env.Type, protocol.MsgClientRegister)
	}

	// Decode the inner payload back to ClientRegister.
	var decoded protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&decoded); err != nil {
		t.Fatalf("failed to decode ClientRegister payload: %v", err)
	}
	if decoded.AuthToken != original.AuthToken {
		t.Errorf("AuthToken = %q; want %q", decoded.AuthToken, original.AuthToken)
	}
	if decoded.Name != original.Name {
		t.Errorf("Name = %q; want %q", decoded.Name, original.Name)
	}
	if decoded.Version != original.Version {
		t.Errorf("Version = %q; want %q", decoded.Version, original.Version)
	}
}

// TestFramingMaxMessageSize verifies that ReadMessage rejects frames whose
// declared length exceeds MaxMessageSize.
func TestFramingMaxMessageSize(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Write a fake frame whose length header is MaxMessageSize+1.
	go func() {
		oversized := uint32(protocol.MaxMessageSize + 1)
		_ = binary.Write(client, binary.BigEndian, oversized)
		// No body needed — ReadMessage should reject before reading body.
		client.Close()
	}()

	_, err := protocol.ReadMessage(server)
	if err == nil {
		t.Fatal("expected error for oversized message, got nil")
	}
}

// TestFramingTruncatedData verifies that ReadMessage returns an error when the
// connection is closed before the full frame body is delivered.
func TestFramingTruncatedData(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	go func() {
		defer client.Close()

		// Write a length header claiming 64 bytes, but send only 16 bytes of body.
		declaredLen := uint32(64)
		_ = binary.Write(client, binary.BigEndian, declaredLen)
		_, _ = client.Write(make([]byte, 16)) // truncated body
		// Closing client causes io.ErrUnexpectedEOF on the server side.
	}()

	_, err := protocol.ReadMessage(server)
	if err == nil {
		t.Fatal("expected error for truncated data, got nil")
	}

	// The underlying cause must be io.ErrUnexpectedEOF or io.EOF.
	if !isEOFError(err) {
		t.Errorf("expected EOF-related error, got: %v", err)
	}
}

// isEOFError unwraps the chain to check for io.EOF / io.ErrUnexpectedEOF.
func isEOFError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsAny(s, "unexpected EOF", "EOF")
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// TestFramingMultipleMessages verifies sequential WriteMessage / ReadMessage
// calls on the same connection work correctly.
func TestFramingMultipleMessages(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ping := protocol.Ping{Seq: 42}
	pong := protocol.Pong{Seq: 42}

	go func() {
		_ = protocol.WriteMessage(client, protocol.MsgPing, ping)
		_ = protocol.WriteMessage(client, protocol.MsgPong, pong)
	}()

	// Read Ping
	env1, err := protocol.ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage (ping) error: %v", err)
	}
	if env1.Type != protocol.MsgPing {
		t.Errorf("message 1 type = %v; want MsgPing", env1.Type)
	}

	// Read Pong
	env2, err := protocol.ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage (pong) error: %v", err)
	}
	if env2.Type != protocol.MsgPong {
		t.Errorf("message 2 type = %v; want MsgPong", env2.Type)
	}

	// Verify pong payload
	var decodedPong protocol.Pong
	if err := gob.NewDecoder(bytes.NewReader(env2.Payload)).Decode(&decodedPong); err != nil {
		t.Fatalf("decode pong payload: %v", err)
	}
	if decodedPong.Seq != pong.Seq {
		t.Errorf("Pong.Seq = %d; want %d", decodedPong.Seq, pong.Seq)
	}
}

// Ensure io package is used (imported for io.ErrUnexpectedEOF reference).
var _ = io.EOF
