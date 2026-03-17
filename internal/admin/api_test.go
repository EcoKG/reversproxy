package admin_test

// Phase 6 integration tests for the admin API.
//
// Success Criteria:
//   SC1: GET /api/clients returns correct client data after a client connects.
//   SC2: GET /api/tunnels returns correct tunnel data after tunnels are registered.
//   SC2: GET /api/stats returns counters that reflect incremented values.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/starlyn/reversproxy/internal/admin"
	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/stats"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type adminTestEnv struct {
	reg      *control.ClientRegistry
	mgr      *tunnel.Manager
	statsReg *stats.Registry
	global   *stats.ServerStats
	baseURL  string
	cancel   context.CancelFunc
}

func startAdminEnv(t *testing.T) *adminTestEnv {
	t.Helper()

	reg      := control.NewClientRegistry()
	mgr      := tunnel.NewManager()
	statsReg := stats.NewRegistry()
	global   := &stats.ServerStats{}
	log      := logger.New("test-admin")

	ctx, cancel := context.WithCancel(context.Background())

	srv := admin.New(reg, mgr, statsReg, global, log)

	// Use :0 so the OS assigns a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // release so Start can re-bind — we only need the port.

	if err := srv.Start(ctx, addr); err != nil {
		cancel()
		t.Fatalf("admin.Start: %v", err)
	}

	// Wait for the server to be ready (up to 500 ms).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://%s/api/stats", addr))
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return &adminTestEnv{
		reg:      reg,
		mgr:      mgr,
		statsReg: statsReg,
		global:   global,
		baseURL:  fmt.Sprintf("http://%s", addr),
		cancel:   cancel,
	}
}

func (e *adminTestEnv) get(t *testing.T, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(e.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return out
}

// fakeCancel is a no-op for context.CancelFunc.
func fakeCancel() {}

// ---------------------------------------------------------------------------
// SC1: /api/clients
// ---------------------------------------------------------------------------

func TestAdminAPI_Clients_Empty(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	body := env.get(t, "/api/clients")

	clients, ok := body["clients"]
	if !ok {
		t.Fatal("response missing 'clients' key")
	}
	arr, ok := clients.([]any)
	if !ok {
		t.Fatalf("'clients' is not an array: %T", clients)
	}
	if len(arr) != 0 {
		t.Fatalf("expected 0 clients, got %d", len(arr))
	}
}

func TestAdminAPI_Clients_AfterRegister(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	// Manually register two clients via the ClientRegistry.
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	ctx := context.Background()
	c1 := env.reg.Register("alice", "1.2.3.4:1111", conn1, fakeCancel)
	c2 := env.reg.Register("bob",   "5.6.7.8:2222", conn2, func() {})
	_ = ctx
	_ = c2

	body := env.get(t, "/api/clients")

	clients, _ := body["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(clients))
	}

	// Verify the first registered client appears.
	found := false
	for _, raw := range clients {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["id"] == c1.ID && m["name"] == "alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("client alice (id=%s) not found in /api/clients response", c1.ID)
	}
}

// ---------------------------------------------------------------------------
// SC1: /api/tunnels
// ---------------------------------------------------------------------------

func TestAdminAPI_Tunnels_Empty(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	body := env.get(t, "/api/tunnels")

	tunnels, ok := body["tunnels"]
	if !ok {
		t.Fatal("response missing 'tunnels' key")
	}
	arr, ok := tunnels.([]any)
	if !ok {
		t.Fatalf("'tunnels' is not an array: %T", tunnels)
	}
	if len(arr) != 0 {
		t.Fatalf("expected 0 tunnels, got %d", len(arr))
	}
}

func TestAdminAPI_Tunnels_AfterRegister(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	// Add a TCP tunnel and an HTTP tunnel to the manager.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	dir := t.TempDir()
	cert, err := control.LoadOrGenerateCert(
		filepath.Join(dir, "s.crt"),
		filepath.Join(dir, "s.key"),
	)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert: %v", err)
	}
	_ = cert

	tcpEntry := env.mgr.AddTunnel("tunnel-tcp-1", "client-1", "127.0.0.1", 8080, 10080, ln)
	httpEntry := env.mgr.AddHTTPTunnel("tunnel-http-1", "client-1", "myapp.local", "127.0.0.1", 3000)
	httpsEntry := env.mgr.AddHTTPSTunnel("tunnel-https-1", "client-2", "secure.local", "127.0.0.1", 4000)

	body := env.get(t, "/api/tunnels")

	tunnels, _ := body["tunnels"].([]any)
	if len(tunnels) != 3 {
		t.Fatalf("expected 3 tunnels, got %d", len(tunnels))
	}

	// Verify each tunnel is present.
	ids := map[string]bool{}
	for _, raw := range tunnels {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ids[m["id"].(string)] = true
	}

	for _, wantID := range []string{tcpEntry.ID, httpEntry.ID, httpsEntry.ID} {
		if !ids[wantID] {
			t.Errorf("tunnel %q not found in /api/tunnels response", wantID)
		}
	}
}

// ---------------------------------------------------------------------------
// SC2: /api/stats
// ---------------------------------------------------------------------------

func TestAdminAPI_Stats_Zeroed(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	body := env.get(t, "/api/stats")

	for _, key := range []string{"total_connections", "active_connections", "bytes_in", "bytes_out"} {
		v, ok := body[key]
		if !ok {
			t.Errorf("stats response missing key %q", key)
			continue
		}
		// JSON numbers decode as float64.
		if v.(float64) != 0 {
			t.Errorf("expected %q == 0, got %v", key, v)
		}
	}
}

func TestAdminAPI_Stats_Increments(t *testing.T) {
	env := startAdminEnv(t)
	defer env.cancel()

	// Directly manipulate the global counters the admin server holds.
	env.global.TotalConnections.Add(5)
	env.global.ActiveConnections.Add(2)
	env.global.BytesIn.Add(1024)
	env.global.BytesOut.Add(2048)

	body := env.get(t, "/api/stats")

	checks := map[string]float64{
		"total_connections":  5,
		"active_connections": 2,
		"bytes_in":           1024,
		"bytes_out":          2048,
	}
	for key, want := range checks {
		got, ok := body[key].(float64)
		if !ok {
			t.Errorf("key %q: unexpected type %T", key, body[key])
			continue
		}
		if got != want {
			t.Errorf("key %q: got %v, want %v", key, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Stats counters unit tests
// ---------------------------------------------------------------------------

func TestStats_AtomicCounters(t *testing.T) {
	gs := &stats.ServerStats{}

	gs.TotalConnections.Add(10)
	gs.ActiveConnections.Add(3)
	gs.BytesIn.Add(512)
	gs.BytesOut.Add(1024)

	if gs.TotalConnections.Load() != 10 {
		t.Errorf("TotalConnections: got %d, want 10", gs.TotalConnections.Load())
	}
	if gs.ActiveConnections.Load() != 3 {
		t.Errorf("ActiveConnections: got %d, want 3", gs.ActiveConnections.Load())
	}
	if gs.BytesIn.Load() != 512 {
		t.Errorf("BytesIn: got %d, want 512", gs.BytesIn.Load())
	}
	if gs.BytesOut.Load() != 1024 {
		t.Errorf("BytesOut: got %d, want 1024", gs.BytesOut.Load())
	}

	gs.ActiveConnections.Add(-1)
	if gs.ActiveConnections.Load() != 2 {
		t.Errorf("ActiveConnections after decrement: got %d, want 2", gs.ActiveConnections.Load())
	}
}

func TestStats_TunnelRegistry(t *testing.T) {
	reg := stats.NewRegistry()

	ts := reg.GetOrCreate("tun-1")
	ts.ConnectionCount.Add(3)
	ts.BytesIn.Add(100)
	ts.BytesOut.Add(200)

	snap := reg.Snapshot()
	s, ok := snap["tun-1"]
	if !ok {
		t.Fatal("tun-1 not found in snapshot")
	}
	if s.ConnectionCount != 3 {
		t.Errorf("ConnectionCount: got %d, want 3", s.ConnectionCount)
	}
	if s.BytesIn != 100 {
		t.Errorf("BytesIn: got %d, want 100", s.BytesIn)
	}
	if s.BytesOut != 200 {
		t.Errorf("BytesOut: got %d, want 200", s.BytesOut)
	}

	// GetOrCreate should return the same instance.
	same := reg.GetOrCreate("tun-1")
	if same != ts {
		t.Error("GetOrCreate returned a different instance for the same key")
	}
}

func TestStats_CountedReaderWriter(t *testing.T) {
	gs := &stats.ServerStats{}
	ts := &stats.TunnelStats{}
	reg := stats.NewRegistry()
	reg.GetOrCreate("tun-counted")

	// Write 10 bytes through a CountedWriter.
	pr, pw := net.Pipe()
	defer pr.Close()
	defer pw.Close()

	cw := stats.NewCountedWriter(pw, ts, gs)

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 10)
		pr.Read(buf)
		close(done)
	}()

	n, err := cw.Write([]byte("hello12345"))
	if err != nil {
		t.Fatalf("CountedWriter.Write: %v", err)
	}
	if n != 10 {
		t.Errorf("CountedWriter: wrote %d bytes, want 10", n)
	}
	<-done

	if gs.BytesOut.Load() != 10 {
		t.Errorf("global BytesOut: got %d, want 10", gs.BytesOut.Load())
	}
	if ts.BytesOut.Load() != 10 {
		t.Errorf("tunnel BytesOut: got %d, want 10", ts.BytesOut.Load())
	}
}
