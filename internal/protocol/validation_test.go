package protocol

import (
	"bytes"
	"encoding/gob"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateDomain
// ---------------------------------------------------------------------------

func TestValidateDomain_Empty(t *testing.T) {
	if err := ValidateDomain(""); err == nil {
		t.Fatal("expected error for empty domain, got nil")
	}
}

func TestValidateDomain_TooLong(t *testing.T) {
	// Build a domain that exceeds 253 characters.
	label := strings.Repeat("a", 63)
	domain := label + "." + label + "." + label + "." + label + "x" // > 253
	if err := ValidateDomain(domain); err == nil {
		t.Fatalf("expected error for domain longer than 253 chars, got nil (len=%d)", len(domain))
	}
}

func TestValidateDomain_LabelTooLong(t *testing.T) {
	label64 := strings.Repeat("a", 64)
	if err := ValidateDomain(label64 + ".com"); err == nil {
		t.Fatal("expected error for label > 63 chars, got nil")
	}
}

func TestValidateDomain_HyphenStart(t *testing.T) {
	if err := ValidateDomain("-bad.example.com"); err == nil {
		t.Fatal("expected error for label starting with hyphen, got nil")
	}
}

func TestValidateDomain_HyphenEnd(t *testing.T) {
	if err := ValidateDomain("bad-.example.com"); err == nil {
		t.Fatal("expected error for label ending with hyphen, got nil")
	}
}

func TestValidateDomain_ValidIPv4(t *testing.T) {
	if err := ValidateDomain("192.168.1.1"); err != nil {
		t.Fatalf("unexpected error for valid IPv4: %v", err)
	}
}

func TestValidateDomain_ValidIPv6(t *testing.T) {
	if err := ValidateDomain("::1"); err != nil {
		t.Fatalf("unexpected error for valid IPv6 loopback: %v", err)
	}
	if err := ValidateDomain("2001:db8::1"); err != nil {
		t.Fatalf("unexpected error for valid IPv6: %v", err)
	}
}

func TestValidateDomain_ValidFQDN(t *testing.T) {
	cases := []string{
		"example.com",
		"sub.example.com",
		"a",
		"xn--nxasmq6b.com",
		"my-host.internal",
	}
	for _, tc := range cases {
		if err := ValidateDomain(tc); err != nil {
			t.Errorf("ValidateDomain(%q): unexpected error: %v", tc, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ValidatePort
// ---------------------------------------------------------------------------

func TestValidatePort_Zero(t *testing.T) {
	if err := ValidatePort(0, 1); err == nil {
		t.Fatal("expected error for port 0 with minPort=1, got nil")
	}
}

func TestValidatePort_Negative(t *testing.T) {
	if err := ValidatePort(-1, 1); err == nil {
		t.Fatal("expected error for negative port, got nil")
	}
}

func TestValidatePort_TooHigh(t *testing.T) {
	if err := ValidatePort(65536, 1); err == nil {
		t.Fatal("expected error for port 65536, got nil")
	}
}

func TestValidatePort_Valid(t *testing.T) {
	cases := []struct{ port, min int }{
		{1, 1},
		{80, 1},
		{443, 1},
		{65535, 1},
		{1024, 1024},
	}
	for _, tc := range cases {
		if err := ValidatePort(tc.port, tc.min); err != nil {
			t.Errorf("ValidatePort(%d, %d): unexpected error: %v", tc.port, tc.min, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ValidateTarget
// ---------------------------------------------------------------------------

func TestValidateTarget_Valid(t *testing.T) {
	if err := ValidateTarget("example.com", 8080, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTarget_MissingPort(t *testing.T) {
	// Port 0 is below minPort=1.
	if err := ValidateTarget("example.com", 0, 1); err == nil {
		t.Fatal("expected error for port 0 with minPort=1, got nil")
	}
}

// ---------------------------------------------------------------------------
// Decode (generic gob helper)
// ---------------------------------------------------------------------------

func encodeGob(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatalf("gob encode: %v", err)
	}
	return buf.Bytes()
}

func TestDecode_ValidPayload(t *testing.T) {
	type simple struct{ X int }
	payload := encodeGob(t, simple{X: 42})
	got, err := Decode[simple](payload)
	if err != nil {
		t.Fatalf("Decode: unexpected error: %v", err)
	}
	if got.X != 42 {
		t.Fatalf("Decode: got X=%d, want 42", got.X)
	}
}

func TestDecode_InvalidPayload(t *testing.T) {
	_, err := Decode[Ping]([]byte("not-valid-gob"))
	if err == nil {
		t.Fatal("expected error decoding invalid gob payload, got nil")
	}
}

func TestDecode_TypeMismatch(t *testing.T) {
	// Encode a Pong but decode as Ping — gob is lenient about missing fields,
	// but encoding a completely different type should still produce either zero
	// or an error. We just check it doesn't panic and optionally returns an error.
	type other struct{ Unrelated string }
	payload := encodeGob(t, other{Unrelated: "hello"})
	// Decode into Ping; gob will produce zero-value since fields don't match.
	got, err := Decode[Ping](payload)
	// Either an error or a zero Ping is acceptable — the key property is that
	// it must not panic.
	_ = err
	_ = got
}
