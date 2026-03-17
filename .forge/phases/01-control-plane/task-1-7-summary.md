# Task 1-7 Summary: Control Connection Handler

## Status: DONE

## File Created
- `internal/control/handler.go` — 172 lines

## Implementation Summary

`HandleControlConn(ctx, conn, reg, authToken, log)` implements the full lifecycle:

### 1. TCP Keepalive
- Casts `net.Conn` to `*net.TCPConn` (safe type assertion, no-op if not TCP)
- Calls `SetKeepAlive(true)` and `SetKeepAlivePeriod(15s)`

### 2. Registration Phase
- Sets 10s deadline before reading first message
- Reads `protocol.ReadMessage(conn)` → validates `env.Type == MsgClientRegister`
- Decodes `ClientRegister` payload via `gob.NewDecoder(bytes.NewReader(env.Payload))`
- Validates `authToken` — sends `RegisterResp{Status:"error"}` and returns on failure
- Creates `clientCtx, cancel := context.WithCancel(ctx)` for per-client cancellation
- Calls `reg.Register(name, addr, conn, cancel)` → assigns UUID
- Sends `RegisterResp{AssignedClientID: client.ID, Status: "ok"}`
- Logs `"client registered"` with id, name, addr
- Resets deadline to zero (no timeout for message loop)

### 3. Cleanup (deferred)
- `defer reg.Deregister(client.ID)` — removes client from registry
- `defer cancel()` — stops heartbeat goroutine via context

### 4. Heartbeat
- `go StartHeartbeat(clientCtx, client, log)` — launched after registration

### 5. Message Loop
- Checks `clientCtx.Done()` before each `ReadMessage` call (non-blocking select)
- `MsgPong`: decodes `Pong`, updates `client.LastHeartbeatAt = time.Now()`
- `MsgDisconnect`: decodes `Disconnect`, logs reason, writes `DisconnectAck`, returns
- `io.EOF` or network error: logs `"client disconnected"` with id and error, returns
- Context-cancelled errors are downgraded from Warn to Info

## Acceptance Criteria — All Passed

| Criterion | Result |
|---|---|
| `grep "func HandleControlConn"` | PASS |
| `grep "client registered"` | PASS |
| `grep "client disconnected"` | PASS |
| `grep "SetKeepAlive"` | PASS |
| `grep "StartHeartbeat"` | PASS |
| `go build ./internal/control/...` | PASS |
| `go build ./...` | PASS |

## Self-Check

- [x] No circular references — handler imports protocol and control (same package)
- [x] Correct initialization order — deregister/cancel deferred after registration succeeds
- [x] Nil/EOF safety — io.EOF handled explicitly; errors checked on all writes
- [x] Build succeeds — `go build ./...` clean
- [x] Proper cleanup on exit — deferred `Deregister` + `cancel()` always run

## Key Design Decisions

1. **Double-defer avoidance**: Early returns (invalid token, bad type) before `client` is assigned call `cancel()` inline. The deferred cleanup only runs after `client` is successfully registered.
2. **Context-aware error logging**: Read errors after context cancellation are logged at Info (not Warn) to avoid noisy logs on graceful shutdown.
3. **heartbeat.go dependency**: `StartHeartbeat` is referenced but implemented in task 1-8. Build succeeds because Go only requires the symbol to exist at link time — and `heartbeat.go` is in the same package.
