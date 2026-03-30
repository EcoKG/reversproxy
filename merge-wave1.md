# Wave 1 Cherry-Pick Merge Report

## Summary

Successfully cherry-picked commit `9227eca` (refactor(wave1): 코드 리팩토링 6개 태스크 완료)
from branch `worktree-agent-a9442bb4` onto
`vela/각종-버그-수정-및-프로젝트-전체-리팩토링-0038`.

Result commit: `1d4089f`

## Conflicts Resolved (7 files)

| File | Conflict Type | Resolution |
|------|---------------|------------|
| `cmd/client/main.go` | HEAD had inline `handleServerConn` + `clientConnWriter` that Wave 1 extracted to `internal/client/handler.go` | Removed the old inline functions; kept only the `main()` function that calls `client.HandleServerConn` |
| `internal/config/defaults.go` | add/add — HEAD added `RelayBufSize`/`MuxChannelBuffer` buffer constants; Wave 1 added `PongTimeout` and reorganised | Merged both sets of constants into a single unified file |
| `internal/control/handler.go` | Multiple sections: SOCKS relay (inline vs `RelayMuxChannel`), HTTP/HTTPS tunnel handler duplication | Took Wave 1's `handleHTTPTunnelRequest` refactor and `RelayMuxChannel` call; preserved HEAD's `config.MessageReadTimeout` constant usage |
| `internal/control/heartbeat.go` | `client.LastHeartbeatAt` field access (Wave 1) vs `client.LastHeartbeat()` method call (HEAD) | Used HEAD's method-based approach (`client.LastHeartbeat()`) which matches the `ClientRegistry` implementation |
| `internal/socks/client.go` | Inline 3-goroutine relay vs `tunnel.RelayMuxChannel` | Took Wave 1's `RelayMuxChannel` (removed `"io"` import, no longer needed) |
| `internal/socks/httpconnect.go` | Same inline relay pattern | Took Wave 1's `RelayMuxChannel` with `r=br` to preserve buffered reader bytes |
| `internal/socks/portforward.go` | Same inline relay pattern | Took Wave 1's `RelayMuxChannel` |

## Key Decisions

- **heartbeat.go**: Wave 1 tried to use `client.LastHeartbeatAt` as a direct struct field, but the `Client` struct uses an atomic `sync.Pointer` behind accessor methods `LastHeartbeat()` / `SetLastHeartbeat()`. HEAD's method-based approach was correct; kept it.
- **config/defaults.go**: Merged both versions to preserve all constants. HEAD had `HeartbeatStaleThreshold`, `SOCKSDialTimeout`, `RelayBufSize`, `MuxChannelBuffer`; Wave 1 added `PongTimeout`. All are kept.
- **handler.go `handleHTTPTunnelRequest`**: The last conflict block in `handleHTTPTunnelRequest` had `if err :=` on HEAD vs `_ =` on Wave 1 for `protocol.WriteMessage`. Took Wave 1's `_ =` since the error is non-critical (best-effort reply).

## Build & Test Status

| Package | Status |
|---------|--------|
| `internal/config` | PASS |
| `internal/protocol` | PASS |
| `internal/control` | PASS |
| `internal/admin` | PASS |
| `internal/socks` | PASS |
| `internal/client` | no test files |
| `internal/app` | BUILD FAILED (pre-existing, before cherry-pick) |
| `internal/reconnect` | FAIL (pre-existing flaky integration tests) |
| `internal/tunnel` | FAIL (pre-existing flaky integration tests) |

The `internal/app` build failure and integration test failures in `internal/reconnect` / `internal/tunnel` were present on the branch **before** the cherry-pick (verified by checking out commit `765743c`). They are not regressions introduced by this merge.

## Files Changed by Cherry-Pick

- `cmd/client/main.go` — stripped inline `handleServerConn`/`clientConnWriter` (now in `internal/client`)
- `internal/client/handler.go` — **new file**: extracted `HandleServerConn` and `ConnWriter`
- `internal/config/defaults.go` — added `PongTimeout`, merged constant blocks
- `internal/control/handler.go` — refactored `handleHTTPTunnelRequest`, replaced inline relay with `RelayMuxChannel`
- `internal/control/heartbeat.go` — uses `config.HeartbeatInterval` constant (no magic numbers)
- `internal/protocol/framing.go` — added `Decode[T]` generic helper + `encPool` for gob encoding
- `internal/socks/client.go` — replaced 3-goroutine relay with `RelayMuxChannel`; removed `"io"` import
- `internal/socks/httpconnect.go` — same relay refactor
- `internal/socks/portforward.go` — same relay refactor
- `internal/socks/server.go` — uses `config` constants (no magic numbers)
- `internal/tunnel/http_proxy.go` — uses `config` constants (no magic numbers)
- `internal/tunnel/relay_mux.go` — **new file**: `RelayMuxChannel` shared relay implementation
