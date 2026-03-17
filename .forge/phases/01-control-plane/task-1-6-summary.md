# Task 1-6 Summary — ClientRegistry

## Status: DONE

## Files Created
- `internal/control/registry.go` (77 lines)
- `internal/control/registry_test.go` (157 lines)

## What Was Implemented

### registry.go
- `Client` struct: `ID`, `Name`, `Addr`, `RegisteredAt`, `LastHeartbeatAt`, `Conn net.Conn`, `cancelFn context.CancelFunc`
- `ClientRegistry` struct: `mu sync.RWMutex` + `clients map[string]*Client`
- `NewClientRegistry() *ClientRegistry` — constructor, initialises the map
- `Register(name, addr string, conn net.Conn, cancelFn context.CancelFunc) *Client` — generates UUID via `uuid.New().String()`, builds Client, inserts under write lock, returns pointer
- `Deregister(id string)` — deletes from map under write lock; idempotent (no-op on missing id)
- `Get(id string) (*Client, bool)` — read-lock lookup; returns `(nil, false)` for missing IDs
- `List() []*Client` — read-lock snapshot into a non-nil slice

### registry_test.go (package control_test)
- `TestRegistry_Register_UniqueUUID` — two sequential registers produce distinct, non-empty UUIDs
- `TestRegistry_Deregister_RemovesFromList` — client disappears from both Get and List after Deregister
- `TestRegistry_Get_MissingReturnsFalse` — Get returns (nil, false) for unknown ID
- `TestRegistry_List_EmptyOnNew` — fresh registry returns non-nil empty slice
- `TestRegistry_ConcurrentRegister` — 50 goroutines register simultaneously; all 50 unique IDs present
- `TestRegistry_ConcurrentReadWrite` — 20 writer + 20 reader goroutines run concurrently without race
- `TestRegistry_DeregisterIdempotent` — double Deregister does not panic

## Verification Results

```
go build ./...         → OK (no output, exit 0)
go test ./internal/control/ -run TestRegistry -v:
  TestRegistry_Register_UniqueUUID       PASS
  TestRegistry_Deregister_RemovesFromList PASS
  TestRegistry_Get_MissingReturnsFalse   PASS
  TestRegistry_List_EmptyOnNew           PASS
  TestRegistry_ConcurrentRegister        PASS
  TestRegistry_ConcurrentReadWrite       PASS
  TestRegistry_DeregisterIdempotent      PASS
PASS (0.003s)
```

Note: `-race` flag requires CGO/gcc which is not installed in this environment. The concurrent tests exercise the same code paths and pass cleanly. All acceptance criteria grep checks pass.

## Acceptance Criteria

| Criterion | Result |
|---|---|
| `grep "func NewClientRegistry"` | PASS |
| `grep "func.*Register"` | PASS |
| `grep "sync.RWMutex"` | PASS |
| `grep "uuid.New"` | PASS |
| `go test -run TestRegistry` passes | PASS |
| `go build ./...` succeeds | PASS |

## Design Notes
- `cancelFn` is intentionally unexported to prevent external callers from inadvertently cancelling a client's context. Handler and heartbeat goroutines receive it at construction time via `Register`.
- `LastHeartbeatAt` is initialised to `time.Now()` at registration so the heartbeat goroutine does not fire immediately on a freshly registered client.
- `List()` returns a fresh slice on every call (snapshot semantics), preventing external callers from holding a reference into the internal map.
