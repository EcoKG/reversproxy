# Task 1-8 Summary — Heartbeat System

## Status: DONE

## File Created
- `internal/control/heartbeat.go` (50 lines)

## What Was Implemented

### `StartHeartbeat(ctx context.Context, client *Client, log *slog.Logger)`
- Exported function in `package control`
- Creates a `time.NewTicker(pingInterval)` (30s), defers `ticker.Stop()` — no goroutine leak
- Tracks `missed int` locally (no shared state, single goroutine owns the counter — thread-safe by design)
- `for-select` loop with two cases:
  - `<-ctx.Done()`: returns immediately on context cancellation
  - `<-ticker.C`: timeout check, then sends `Ping{Seq: uint64(missed)}`

### Timeout Logic
- Condition: `time.Since(client.LastHeartbeatAt) > pingInterval*(maxMissed+1) && missed >= maxMissed`
- Logs `"heartbeat timeout"` at Warn level with `"id"` field
- Calls `client.cancelFn()` to propagate disconnect through context tree, then returns

### Ping Write Failure Path
- If `protocol.WriteMessage` returns an error: logs `"ping write failed"`, calls `client.cancelFn()`, returns

### Constants
| Constant | Value |
|---|---|
| `pingInterval` | `30 * time.Second` |
| `pongTimeout` | `10 * time.Second` |
| `maxMissed` | `2` |

### Pong Integration
`missed` is incremented on every tick. When `handler.go` receives a `MsgPong` it resets `client.LastHeartbeatAt = time.Now()`, which causes the timeout condition to evaluate false — effectively resetting the miss window without needing a shared counter.

## Acceptance Criteria Results
| Criterion | Result |
|---|---|
| `grep "func StartHeartbeat"` | PASS |
| `grep "heartbeat timeout"` | PASS |
| `grep "pingInterval"` | PASS |
| `grep "cancelFn"` | PASS |
| `go build ./internal/control/...` | PASS |
| `go build ./...` | PASS |

## Design Notes
- No mutex needed on `missed` — single goroutine owns the ticker loop
- `pongTimeout` constant is defined for completeness (referenced in plan) but the actual timeout is enforced implicitly via the `LastHeartbeatAt` staleness check
- `ctx.Done()` as the first select case ensures the goroutine exits cleanly on server shutdown or client deregistration without leaking
