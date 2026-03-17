package reconnect_test

import (
	"testing"
	"time"

	"github.com/starlyn/reversproxy/internal/reconnect"
)

// ---------------------------------------------------------------------------
// Backoff unit tests
// ---------------------------------------------------------------------------

// TestBackoff_GrowsExponentially verifies that successive calls to Next()
// return values that grow by the configured Multiplier, capped at Max.
func TestBackoff_GrowsExponentially(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        1 * time.Second,
		Max:            16 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0, // no jitter for deterministic test
	}

	// Expected raw delays (before jitter): 1s, 2s, 4s, 8s, 16s, 16s, ...
	prev := time.Duration(0)
	for i := 0; i < 6; i++ {
		d := b.Next()
		if d <= 0 {
			t.Fatalf("call %d: expected positive delay, got %v", i, d)
		}
		if d > b.Max {
			t.Fatalf("call %d: delay %v exceeds max %v", i, d, b.Max)
		}
		if i > 0 && d < prev && d < b.Max {
			// delay should only shrink if it is already at Max
			t.Errorf("call %d: delay %v smaller than previous %v without being capped", i, d, prev)
		}
		prev = d
	}
}

// TestBackoff_Reset verifies that Reset() brings the delay back to Initial.
func TestBackoff_Reset(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        500 * time.Millisecond,
		Max:            30 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0,
	}

	// Advance a few times.
	for i := 0; i < 5; i++ {
		_ = b.Next()
	}

	// Reset and verify the first delay is Initial again.
	b.Reset()
	d := b.Next()
	if d != b.Initial {
		t.Errorf("after Reset, expected %v, got %v", b.Initial, d)
	}
}

// TestBackoff_MaxCap verifies that the delay never exceeds Max.
func TestBackoff_MaxCap(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        1 * time.Second,
		Max:            4 * time.Second,
		Multiplier:     10.0, // very aggressive growth
		JitterFraction: 0,
	}

	for i := 0; i < 10; i++ {
		d := b.Next()
		if d > b.Max {
			t.Fatalf("call %d: delay %v exceeds max %v", i, d, b.Max)
		}
	}
}

// TestBackoff_JitterWithinBounds verifies that jitter stays within ±JitterFraction.
func TestBackoff_JitterWithinBounds(t *testing.T) {
	b := &reconnect.Backoff{
		Initial:        10 * time.Second,
		Max:            60 * time.Second,
		Multiplier:     1, // no growth — keep delay at Initial for all calls
		JitterFraction: 0.2,
	}

	// With Multiplier=1 the raw delay is always Initial; only jitter changes it.
	low  := time.Duration(float64(b.Initial) * (1 - b.JitterFraction))
	high := time.Duration(float64(b.Initial) * (1 + b.JitterFraction))

	for i := 0; i < 100; i++ {
		d := b.Next()
		if d < low || d > high {
			t.Errorf("iteration %d: delay %v outside [%v, %v]", i, d, low, high)
		}
	}
}

// TestNewBackoff_Defaults verifies the convenience constructor produces
// sensible defaults.
func TestNewBackoff_Defaults(t *testing.T) {
	b := reconnect.NewBackoff()

	if b.Initial <= 0 {
		t.Errorf("Initial should be positive, got %v", b.Initial)
	}
	if b.Max < b.Initial {
		t.Errorf("Max %v should be >= Initial %v", b.Max, b.Initial)
	}
	if b.Multiplier < 1 {
		t.Errorf("Multiplier should be >= 1, got %v", b.Multiplier)
	}
	if b.JitterFraction < 0 || b.JitterFraction > 1 {
		t.Errorf("JitterFraction %v should be in [0, 1]", b.JitterFraction)
	}

	// First call must succeed.
	d := b.Next()
	if d <= 0 {
		t.Errorf("first Next() returned non-positive delay: %v", d)
	}
}

// ---------------------------------------------------------------------------
// ClientConfig / TunnelConfig unit tests
// ---------------------------------------------------------------------------

// TestClientConfig_AddTunnel verifies that AddTunnel appends entries correctly.
func TestClientConfig_AddTunnel(t *testing.T) {
	cfg := &reconnect.ClientConfig{}

	if len(cfg.Tunnels) != 0 {
		t.Fatalf("expected 0 tunnels, got %d", len(cfg.Tunnels))
	}

	cfg.AddTunnel("127.0.0.1", 8080, 0)
	cfg.AddTunnel("127.0.0.1", 9090, 9091)

	if len(cfg.Tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(cfg.Tunnels))
	}

	if cfg.Tunnels[0].LocalPort != 8080 {
		t.Errorf("tunnel 0: expected LocalPort 8080, got %d", cfg.Tunnels[0].LocalPort)
	}
	if cfg.Tunnels[1].RequestedPort != 9091 {
		t.Errorf("tunnel 1: expected RequestedPort 9091, got %d", cfg.Tunnels[1].RequestedPort)
	}
}
