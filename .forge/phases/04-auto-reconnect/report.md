# Phase 4 Report: 자동 재연결 및 세션 복구

## Verdict: PASS

## Files Created/Modified

### New files
| Path | Description |
|------|-------------|
| `internal/reconnect/backoff.go` | `Backoff` struct: exponential backoff with configurable initial/max/multiplier and ±20% jitter. `NewBackoff()` convenience constructor. Thread-safe. |
| `internal/reconnect/config.go` | `TunnelConfig` and `ClientConfig` structs. `ClientConfig.AddTunnel()` accumulates tunnel registrations so the client can replay them after every reconnect. |
| `internal/reconnect/reconnect_test.go` | Unit tests for Backoff (growth, cap, reset, jitter bounds, defaults) and ClientConfig. |
| `internal/reconnect/integration_test.go` | Integration tests covering SC1, SC2, SC3 using a `restartableServer` helper. |

### Modified files
| Path | Change |
|------|--------|
| `cmd/client/main.go` | Full rewrite: extracted connection logic into `connect()`, wrapped in a `for` reconnect loop driven by `Backoff`, context-aware SIGINT/SIGTERM exit, tunnel state preserved in `ClientConfig` and re-registered after every successful dial. |

### Incidental fix (pre-existing)
| Path | Change |
|------|--------|
| `internal/tunnel/ctrl_conn_registry.go` | Investigated build failure from Phase 3. `ControlConnRegistry` already existed in `ctrl_registry.go` and `copyAndClose` in `https_proxy.go`. Duplicate file I temporarily created was removed; no source change to Phase 3 code. |

## Design Notes

- The reconnect loop lives **entirely in the client** (`cmd/client/main.go`). The server is unchanged.
- `context.Context` propagated from `signal.NotifyContext` is the single cancellation signal for both the inner `connect()` call and the backoff sleep, so SIGINT exits cleanly without waiting for the full backoff delay.
- Jitter is applied as `delay × JitterFraction × rand[-1, 1]`, which gives ±20% spread. This prevents thundering-herd when many clients restart simultaneously.
- The server cleanly handles a client reconnecting under the same name because `ClientRegistry.Register` issues a fresh UUID each time and the old entry is deregistered when the old connection drops.
- `tls.Dialer.DialContext` is used so the dial itself respects context cancellation (no goroutine leak on SIGINT during a slow dial).

## Tests

| Test | Type | Result |
|------|------|--------|
| `TestBackoff_GrowsExponentially` | Unit | PASS |
| `TestBackoff_Reset` | Unit | PASS |
| `TestBackoff_MaxCap` | Unit | PASS |
| `TestBackoff_JitterWithinBounds` | Unit | PASS |
| `TestNewBackoff_Defaults` | Unit | PASS |
| `TestClientConfig_AddTunnel` | Unit | PASS |
| `TestSC1_AutoReconnectAfterServerRestart` | Integration | PASS |
| `TestSC2_TunnelRestoredAfterReconnect` | Integration | PASS |
| `TestSC3_ExponentialBackoff` | Integration | PASS |

All prior tests (control, protocol, tunnel packages) continue to pass.

## Success Criteria

- **SC1: PASS** — `TestSC1_AutoReconnectAfterServerRestart` demonstrates that after the server stops and restarts on a new port, the reconnect loop dials again and the client is re-registered within the 10 s deadline.
- **SC2: PASS** — `TestSC2_TunnelRestoredAfterReconnect` demonstrates that after reconnecting, `ClientConfig.Tunnels` are re-registered via `MsgRequestTunnel` and external traffic through the new tunnel works correctly (echo round-trip verified).
- **SC3: PASS** — `TestSC3_ExponentialBackoff` and the unit tests verify that `Backoff.Next()` doubles each call, is capped at `Max`, resets to `Initial` after `Reset()`, and that jitter stays within the configured ±20% band.
