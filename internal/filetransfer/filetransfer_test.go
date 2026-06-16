package filetransfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mkFile(t *testing.T, dir, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	return p, data
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// startRecv spins up a receiver on a loopback port and returns its endpoint URL.
func startRecv(t *testing.T, dropDir, token string) string {
	t.Helper()
	recv, err := NewReceiver(dropDir, token, 0, quietLog())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr, err := StartReceiver(ctx, "127.0.0.1:0", recv)
	if err != nil {
		t.Fatalf("StartReceiver: %v", err)
	}
	return "http://" + addr
}

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	drop := t.TempDir()
	srcPath, data := mkFile(t, src, "report.bin", 256*1024+777)

	endpoint := startRecv(t, drop, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := SendFile(ctx, endpoint, srcPath, SendOptions{}, quietLog()); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(drop, "report.bin"))
	if err != nil {
		t.Fatalf("read dropped: %v", err)
	}
	if sha(got) != sha(data) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(data))
	}
	if _, err := os.Stat(filepath.Join(drop, "report.bin.part")); !os.IsNotExist(err) {
		t.Fatalf(".part file should be gone after finalize")
	}
}

// TestResume seeds a partial .part file, then sends — the sender must resume
// from the existing offset and still produce the correct final file.
func TestResume(t *testing.T) {
	src := t.TempDir()
	drop := t.TempDir()
	srcPath, data := mkFile(t, src, "big.dat", 200*1024)

	// Pre-seed the first half as if a previous transfer was interrupted.
	half := len(data) / 2
	if err := os.WriteFile(filepath.Join(drop, "big.dat.part"), data[:half], 0o644); err != nil {
		t.Fatalf("seed part: %v", err)
	}

	endpoint := startRecv(t, drop, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := SendFile(ctx, endpoint, srcPath, SendOptions{}, quietLog()); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(drop, "big.dat"))
	if err != nil {
		t.Fatalf("read dropped: %v", err)
	}
	if sha(got) != sha(data) {
		t.Fatalf("resumed content mismatch")
	}
}

// TestStalePartSelfHeal seeds an OVERSIZED leftover .part (as if a previous,
// larger transfer of the same name aborted), then sends a smaller file. The
// receiver must truncate the stale part and still deliver the correct file
// rather than 409-looping forever.
func TestStalePartSelfHeal(t *testing.T) {
	src := t.TempDir()
	drop := t.TempDir()
	srcPath, data := mkFile(t, src, "doc.bin", 50*1024)

	junk := make([]byte, 80*1024) // larger than the file we're about to send
	for i := range junk {
		junk[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(drop, "doc.bin.part"), junk, 0o644); err != nil {
		t.Fatalf("seed oversized part: %v", err)
	}

	endpoint := startRecv(t, drop, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := SendFile(ctx, endpoint, srcPath, SendOptions{MaxRetries: 3, RetryWait: 10 * time.Millisecond}, quietLog()); err != nil {
		t.Fatalf("SendFile should self-heal an oversized .part: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(drop, "doc.bin"))
	if err != nil {
		t.Fatalf("read dropped: %v", err)
	}
	if sha(got) != sha(data) {
		t.Fatalf("self-healed content mismatch")
	}
	if _, err := os.Stat(filepath.Join(drop, "doc.bin.part")); !os.IsNotExist(err) {
		t.Fatalf(".part should be gone after finalize")
	}
}

func TestTokenRequired(t *testing.T) {
	src := t.TempDir()
	drop := t.TempDir()
	srcPath, _ := mkFile(t, src, "secret.bin", 4096)

	endpoint := startRecv(t, drop, "s3cr3t")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wrong token → should fail and drop nothing.
	err := SendFile(ctx, endpoint, srcPath, SendOptions{Token: "wrong", MaxRetries: 1, RetryWait: 10 * time.Millisecond}, quietLog())
	if err == nil {
		t.Fatalf("expected failure with wrong token")
	}
	if _, statErr := os.Stat(filepath.Join(drop, "secret.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("file must not be delivered with wrong token")
	}

	// Correct token → succeeds.
	if err := SendFile(ctx, endpoint, srcPath, SendOptions{Token: "s3cr3t"}, quietLog()); err != nil {
		t.Fatalf("SendFile with correct token: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(drop, "secret.bin")); statErr != nil {
		t.Fatalf("file should be delivered with correct token: %v", statErr)
	}
}

func TestDuplicateNamesDoNotClobber(t *testing.T) {
	src := t.TempDir()
	drop := t.TempDir()
	endpoint := startRecv(t, drop, "")
	ctx := context.Background()

	p1, d1 := mkFile(t, src, "dup.txt", 1000)
	if err := SendFile(ctx, endpoint, p1, SendOptions{}, quietLog()); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	// Different content, same name.
	p2, d2 := mkFile(t, src, "dup.txt", 2000)
	if err := SendFile(ctx, endpoint, p2, SendOptions{}, quietLog()); err != nil {
		t.Fatalf("send 2: %v", err)
	}

	first, _ := os.ReadFile(filepath.Join(drop, "dup.txt"))
	second, err := os.ReadFile(filepath.Join(drop, "dup (1).txt"))
	if err != nil {
		t.Fatalf("second copy missing: %v", err)
	}
	if sha(first) != sha(d1) || sha(second) != sha(d2) {
		t.Fatalf("duplicate handling corrupted content")
	}
}

func TestSanitizeName(t *testing.T) {
	// Inputs that must be rejected outright.
	mustError := []string{"", "  ", ".", "..", "/", `\`}
	for _, in := range mustError {
		if got, err := sanitizeName(in); err == nil {
			t.Errorf("sanitizeName(%q) = %q; want error", in, got)
		}
	}

	// Traversal/path inputs must be neutralised to a safe base name (NOT errored):
	// the file then lands inside the drop dir and cannot escape it.
	reduce := map[string]string{
		"normal.zip":          "normal.zip",
		"a/b.txt":             "b.txt",
		`c\d.txt`:             "d.txt",
		"../../etc/passwd":    "passwd",
		`..\..\windows\a.ini`: "a.ini",
	}
	for in, want := range reduce {
		got, err := sanitizeName(in)
		if err != nil || got != want {
			t.Errorf("sanitizeName(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
}
