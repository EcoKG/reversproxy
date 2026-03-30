package stats_test

// Task 3.7 — stats package unit tests.
//
// Tests cover:
//   TestCountedReader_ReadCount    : bytes read are counted on both global and tunnel stats.
//   TestCountedReader_ReadError    : a read error is propagated and counts are still updated.
//   TestCountedReader_Concurrent   : race-detector safety for concurrent reads.
//   TestCountedWriter_WriteCount   : bytes written are counted on both global and tunnel stats.
//   TestCountedWriter_Concurrent   : race-detector safety for concurrent writes.
//   TestRegistry_GetOrCreate       : same key returns the same *TunnelStats instance.
//   TestRegistry_Delete            : deleted keys are absent from Snapshot.
//   TestServerStats_AddLoad        : Add/Load are consistent under no concurrency.

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/EcoKG/reversproxy/internal/stats"
)

// ---------------------------------------------------------------------------
// TestCountedReader_ReadCount
// ---------------------------------------------------------------------------

// TestCountedReader_ReadCount verifies that bytes read through a CountedReader
// are reflected in both the global ServerStats and the per-tunnel TunnelStats.
func TestCountedReader_ReadCount(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}

	data := []byte("hello world") // 11 bytes
	cr := stats.NewCountedReader(bytes.NewReader(data), ts, gs)

	out, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("data mismatch: got %q, want %q", out, data)
	}

	if got := gs.BytesIn.Load(); got != int64(len(data)) {
		t.Errorf("global BytesIn: got %d, want %d", got, len(data))
	}
	if got := ts.BytesIn.Load(); got != int64(len(data)) {
		t.Errorf("tunnel BytesIn: got %d, want %d", got, len(data))
	}

	// BytesOut must remain 0 — we only read.
	if got := gs.BytesOut.Load(); got != 0 {
		t.Errorf("global BytesOut should be 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestCountedReader_ReadError
// ---------------------------------------------------------------------------

// errReader is an io.Reader that returns a fixed error after some bytes.
type errReader struct {
	data []byte
	pos  int
	err  error
}

func (e *errReader) Read(p []byte) (int, error) {
	if e.pos >= len(e.data) {
		return 0, e.err
	}
	n := copy(p, e.data[e.pos:])
	e.pos += n
	if e.pos >= len(e.data) {
		return n, e.err
	}
	return n, nil
}

// TestCountedReader_ReadError verifies that a read error is propagated and
// any bytes already read are still counted.
func TestCountedReader_ReadError(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}

	sentinel := errors.New("simulated read error")
	data := []byte("partial")
	cr := stats.NewCountedReader(&errReader{data: data, err: sentinel}, ts, gs)

	buf := make([]byte, 16)
	n, err := cr.Read(buf)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
	if int64(n) != gs.BytesIn.Load() {
		t.Errorf("global BytesIn: got %d, want %d", gs.BytesIn.Load(), n)
	}
	if int64(n) != ts.BytesIn.Load() {
		t.Errorf("tunnel BytesIn: got %d, want %d", ts.BytesIn.Load(), n)
	}
}

// ---------------------------------------------------------------------------
// TestCountedReader_Concurrent
// ---------------------------------------------------------------------------

// TestCountedReader_Concurrent exercises CountedReader under concurrent reads
// to verify race-detector safety.  Run with: go test -race ./internal/stats/...
func TestCountedReader_Concurrent(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}

	const workers = 16
	const chunk = 64

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			cr := stats.NewCountedReader(bytes.NewReader(bytes.Repeat([]byte("x"), chunk)), ts, gs)
			io.ReadAll(cr)
		}()
	}
	wg.Wait()

	want := int64(workers * chunk)
	if got := gs.BytesIn.Load(); got != want {
		t.Errorf("global BytesIn: got %d, want %d", got, want)
	}
	if got := ts.BytesIn.Load(); got != want {
		t.Errorf("tunnel BytesIn: got %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// TestCountedWriter_WriteCount
// ---------------------------------------------------------------------------

// TestCountedWriter_WriteCount verifies that bytes written via CountedWriter
// are reflected in BytesOut on both global and per-tunnel stats.
func TestCountedWriter_WriteCount(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}

	pr, pw := net.Pipe()
	defer pr.Close()
	defer pw.Close()

	cw := stats.NewCountedWriter(pw, ts, gs)

	data := []byte("counted write test") // 18 bytes
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, len(data))
		io.ReadFull(pr, buf)
	}()

	n, err := cw.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d bytes, want %d", n, len(data))
	}
	<-done

	if got := gs.BytesOut.Load(); got != int64(len(data)) {
		t.Errorf("global BytesOut: got %d, want %d", got, len(data))
	}
	if got := ts.BytesOut.Load(); got != int64(len(data)) {
		t.Errorf("tunnel BytesOut: got %d, want %d", got, len(data))
	}

	// BytesIn must remain 0 — we only wrote.
	if got := gs.BytesIn.Load(); got != 0 {
		t.Errorf("global BytesIn should be 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestCountedWriter_Concurrent
// ---------------------------------------------------------------------------

// TestCountedWriter_Concurrent exercises CountedWriter under concurrent writes
// to verify race-detector safety.
func TestCountedWriter_Concurrent(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}

	const workers = 16
	const chunk = 64

	// Use a discard writer to avoid blocking.
	cw := stats.NewCountedWriter(io.Discard, ts, gs)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			cw.Write(bytes.Repeat([]byte("y"), chunk))
		}()
	}
	wg.Wait()

	want := int64(workers * chunk)
	if got := gs.BytesOut.Load(); got != want {
		t.Errorf("global BytesOut: got %d, want %d", got, want)
	}
	if got := ts.BytesOut.Load(); got != want {
		t.Errorf("tunnel BytesOut: got %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_GetOrCreate
// ---------------------------------------------------------------------------

// TestRegistry_GetOrCreate verifies that repeated calls with the same tunnelID
// return the same *TunnelStats pointer, while different keys return distinct ones.
func TestRegistry_GetOrCreate(t *testing.T) {
	reg := stats.NewRegistry()

	a1 := reg.GetOrCreate("tunnel-a")
	a2 := reg.GetOrCreate("tunnel-a")
	if a1 != a2 {
		t.Error("GetOrCreate returned different pointers for the same key")
	}

	b := reg.GetOrCreate("tunnel-b")
	if a1 == b {
		t.Error("GetOrCreate returned same pointer for different keys")
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Delete
// ---------------------------------------------------------------------------

// TestRegistry_Delete verifies that after Delete, the key is absent from
// Snapshot and Get returns (nil, false).
func TestRegistry_Delete(t *testing.T) {
	reg := stats.NewRegistry()

	ts := reg.GetOrCreate("ephemeral")
	ts.ConnectionCount.Add(7)

	snap := reg.Snapshot()
	if _, ok := snap["ephemeral"]; !ok {
		t.Fatal("expected ephemeral in snapshot before delete")
	}

	reg.Delete("ephemeral")

	snap = reg.Snapshot()
	if _, ok := snap["ephemeral"]; ok {
		t.Error("expected ephemeral absent from snapshot after delete")
	}

	if got, ok := reg.Get("ephemeral"); ok || got != nil {
		t.Errorf("Get after Delete: got (%v, %v), want (nil, false)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// TestServerStats_AddLoad
// ---------------------------------------------------------------------------

// TestServerStats_AddLoad verifies basic Add/Load semantics on all counters.
func TestServerStats_AddLoad(t *testing.T) {
	gs := &stats.ServerStats{}

	gs.TotalConnections.Add(1)
	gs.ActiveConnections.Add(2)
	gs.BytesIn.Add(300)
	gs.BytesOut.Add(400)

	checks := []struct {
		name string
		got  int64
		want int64
	}{
		{"TotalConnections", gs.TotalConnections.Load(), 1},
		{"ActiveConnections", gs.ActiveConnections.Load(), 2},
		{"BytesIn", gs.BytesIn.Load(), 300},
		{"BytesOut", gs.BytesOut.Load(), 400},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}

	// Decrement ActiveConnections.
	gs.ActiveConnections.Add(-1)
	if got := gs.ActiveConnections.Load(); got != 1 {
		t.Errorf("ActiveConnections after decrement: got %d, want 1", got)
	}
}
