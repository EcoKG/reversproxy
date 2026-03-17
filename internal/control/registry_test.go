package control_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/EcoKG/reversproxy/internal/control"
)

// noopConn returns a net.Conn backed by net.Pipe suitable for tests that do
// not actually send data.
func noopConn(t *testing.T) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a
}

// noopCancel returns a context.CancelFunc that does nothing, for use in tests
// where cancellation behaviour is not under test.
func noopCancel() context.CancelFunc {
	_, cancel := context.WithCancel(context.Background())
	return cancel
}

// TestRegistry_Register_UniqueUUID verifies that successive calls to Register
// each produce a distinct UUID.
func TestRegistry_Register_UniqueUUID(t *testing.T) {
	reg := control.NewClientRegistry()

	c1 := reg.Register("alice", "1.2.3.4:1000", noopConn(t), noopCancel())
	c2 := reg.Register("bob", "1.2.3.4:1001", noopConn(t), noopCancel())

	if c1.ID == "" {
		t.Fatal("expected non-empty ID for c1")
	}
	if c2.ID == "" {
		t.Fatal("expected non-empty ID for c2")
	}
	if c1.ID == c2.ID {
		t.Fatalf("expected distinct IDs, got %q for both", c1.ID)
	}
}

// TestRegistry_Deregister_RemovesFromList verifies that Deregister causes the
// client to disappear from List and Get.
func TestRegistry_Deregister_RemovesFromList(t *testing.T) {
	reg := control.NewClientRegistry()

	client := reg.Register("alice", "1.2.3.4:1000", noopConn(t), noopCancel())
	id := client.ID

	if _, ok := reg.Get(id); !ok {
		t.Fatal("client should be present before Deregister")
	}

	reg.Deregister(id)

	if _, ok := reg.Get(id); ok {
		t.Fatalf("client %q should have been removed by Deregister", id)
	}

	for _, c := range reg.List() {
		if c.ID == id {
			t.Fatalf("client %q still appears in List after Deregister", id)
		}
	}
}

// TestRegistry_Get_MissingReturnsFalse verifies that Get returns false when
// the requested id does not exist in the registry.
func TestRegistry_Get_MissingReturnsFalse(t *testing.T) {
	reg := control.NewClientRegistry()

	if c, ok := reg.Get("nonexistent-id"); ok || c != nil {
		t.Fatalf("expected (nil, false) for missing id, got (%v, %v)", c, ok)
	}
}

// TestRegistry_List_EmptyOnNew verifies that a fresh registry has an empty
// (but non-nil) List.
func TestRegistry_List_EmptyOnNew(t *testing.T) {
	reg := control.NewClientRegistry()
	list := reg.List()
	if list == nil {
		t.Fatal("List() must never return nil")
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(list))
	}
}

// TestRegistry_ConcurrentRegister verifies race-freedom when multiple
// goroutines register clients simultaneously. Run with -race.
func TestRegistry_ConcurrentRegister(t *testing.T) {
	const goroutines = 50

	reg := control.NewClientRegistry()

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			reg.Register("concurrent", "127.0.0.1:0", noopConn(t), noopCancel())
		}()
	}
	wg.Wait()

	list := reg.List()
	if len(list) != goroutines {
		t.Fatalf("expected %d clients after concurrent register, got %d", goroutines, len(list))
	}

	// All IDs must be unique.
	seen := make(map[string]struct{}, len(list))
	for _, c := range list {
		if _, dup := seen[c.ID]; dup {
			t.Fatalf("duplicate ID detected: %q", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
}

// TestRegistry_ConcurrentReadWrite verifies race-freedom when readers and
// writers operate on the registry simultaneously. Run with -race.
func TestRegistry_ConcurrentReadWrite(t *testing.T) {
	reg := control.NewClientRegistry()

	// Pre-populate a handful of entries.
	for i := 0; i < 5; i++ {
		reg.Register("seed", "127.0.0.1:0", noopConn(t), noopCancel())
	}

	var wg sync.WaitGroup
	const workers = 20

	// Writers: concurrently register more clients.
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			reg.Register("writer", "127.0.0.1:0", noopConn(t), noopCancel())
		}()
	}

	// Readers: concurrently iterate the list.
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_ = reg.List()
		}()
	}

	wg.Wait()
}

// TestRegistry_DeregisterIdempotent verifies that calling Deregister twice for
// the same id does not panic.
func TestRegistry_DeregisterIdempotent(t *testing.T) {
	reg := control.NewClientRegistry()
	client := reg.Register("alice", "127.0.0.1:0", noopConn(t), noopCancel())

	reg.Deregister(client.ID)
	reg.Deregister(client.ID) // second call must not panic
}
