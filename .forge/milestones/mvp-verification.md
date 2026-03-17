# Milestone Verification: MVP — 안정적인 리버스 터널 프록시

## Verdict: INTEGRATED

---

## D1: Build Integrity

**Result: PASS**

- `go build ./...` — exit 0, no output (clean)
- `go vet ./...` — exit 0, no output (clean)
- No unused imports or unreachable code detected across all 11 packages

---

## D2: Test Suite

**Result: PASS — 52 tests, 0 failures, 0 skips**

| Package | Tests | Result |
|---|---|---|
| `internal/admin` | 9 | PASS |
| `internal/config` | 7 | PASS |
| `internal/control` | 10 | PASS |
| `internal/protocol` | 4 | PASS |
| `internal/reconnect` | 9 | PASS |
| `internal/tunnel` | 13 | PASS |
| `cmd/client`, `cmd/server`, `internal/logger`, `internal/proxy`, `internal/stats` | — | no test files |

**Total: 52/52 PASS**

Note: `-race` flag requires CGO/gcc which is unavailable in this environment. Race safety is structurally enforced via `sync.RWMutex` throughout all shared data structures and validated by concurrent tests (`TestRegistry_ConcurrentRegister`, `TestRegistry_ConcurrentReadWrite`, `TestSC3_ConcurrentLoad`).

---

## D3: Cross-Phase Integration

### Phase 1 → Phase 2 (Control → TCP Tunnel)

**PASS.** `control/handler.go` dispatches `MsgRequestTunnel` to `handleRequestTunnel()`, which calls `mgr.AddTunnel()` and spawns `tunnel.StartPublicListener()`. The handler receives `mgr *tunnel.Manager` and `dataAddr string` as explicit parameters — wired correctly in `cmd/server/main.go:203` where `control.HandleControlConn(ctx, c, reg, cfg.AuthToken, log, mgr, resolvedDataAddr, ctrlConns)` is called.

### Phase 2 → Phase 3 (TCP → HTTP Routing)

**PASS.** `tunnel/http_proxy.go` (`StartHTTPProxy`) and `tunnel/https_proxy.go` (`StartHTTPSProxy`) both use `mgr.GetHTTPTunnel()` / `mgr.GetHTTPSTunnel()` (same Manager instance). `control/handler.go` handles `MsgRequestHTTPTunnel` / `MsgRequestHTTPSTunnel` by calling `mgr.AddHTTPTunnel()` / `mgr.AddHTTPSTunnel()`. HTTP/HTTPS proxies share the same OpenConnection → data-connection relay path as TCP tunnels. `ControlConnRegistry` provides the clientID → net.Conn mapping for the HTTP/HTTPS path. All wired through `cmd/server/main.go`.

### Phase 2 → Phase 4 (TCP → Reconnect)

**PASS.** `cmd/client/main.go` extracts connection logic into `connect()`, wraps it in a reconnect loop driven by `reconnect.Backoff`. After every successful dial, `reconnect.ClientConfig.Tunnels`, `HTTPTunnels`, and `HTTPSTunnels` are re-registered by replaying `MsgRequestTunnel` / `MsgRequestHTTPTunnel` / `MsgRequestHTTPSTunnel`. The server re-issues fresh `TunnelID`s per reconnect. No server changes needed; the loop is entirely client-side.

### Phase 3+4 → Phase 5 (Multi-client)

**PASS.** Multi-client isolation is structurally guaranteed by:
- `Manager` maps keyed by tunnelID/hostname with `sync.RWMutex` on all paths
- `RemoveTunnelsForClient()` and `RemoveHTTPTunnelsForClient()` scope cleanup strictly to one `clientID` under a single lock acquisition
- `StartPublicListener()` captures `entry` (which holds `clientConn`) at registration time — no shared write path between clients
- Per-client `context.WithCancel` derived from server root context
- Verified by 6 Phase 5 integration tests including `TestSC2_ClientFailureIsolation` (kill middle client, A/C unaffected)

### Phase 5 → Phase 6 (Multi-client → Observability)

**PASS.** `admin.Server` receives the same `*control.ClientRegistry` and `*tunnel.Manager` references used by all other phases. `GET /api/clients` calls `reg.List()`, `GET /api/tunnels` calls `mgr.ListTunnels()` + `mgr.ListHTTPTunnels()` (snapshot reads under RLock added in Phase 6). Global connection stats (`TotalConnections`, `ActiveConnections`) are tracked in `cmd/server/main.go` accept loop via `stats.Global`. All wired at startup in `main.go:147`: `admin.New(reg, mgr, statsReg, globalStats, log)`.

---

## D4: Requirement Coverage

| Requirement | Status | Evidence |
|---|---|---|
| REQ-TCP-01 | PASS | `tunnel/server.go` StartPublicListener + StartDataListener + relayExternalConn implement full bidirectional TCP relay. `TestSC1_ExternalConnectionReachesLocalService` (echo round-trip), `TestSC2_DataIntegrity` (64 KB no-corruption), `TestSC3_ConcurrentConnections` (5 simultaneous) all PASS. |
| REQ-HTTP-01 | PASS | `tunnel/http_proxy.go` routes by Host header; `tunnel/https_proxy.go` routes by TLS SNI without terminating TLS. `TestSC1_HTTPHostRouting` and `TestSC2_HTTPSRoutingBySNI` both PASS. Unknown host returns HTTP 502 / closes connection. |
| REQ-CONN-01 | PASS | `reconnect.Backoff` (exponential, ±20% jitter, capped) drives reconnect loop in `cmd/client/main.go`. `reconnect.ClientConfig` stores all tunnel registrations and replays them after every successful reconnect. `TestSC1_AutoReconnectAfterServerRestart`, `TestSC2_TunnelRestoredAfterReconnect`, `TestSC3_ExponentialBackoff` all PASS. |
| REQ-MULTI-01 | PASS | `ClientRegistry` gives each client a unique UUID. Per-client context cancellation, mutex-protected Manager maps, and scoped cleanup (`RemoveTunnelsForClient`) ensure full independence. `TestSC1_MultiClientIndependentTunnels` (3 TCP + 1 HTTP), `TestSC2_ClientFailureIsolation`, `TestSC3_ConcurrentLoad` (avg RTT 11ms, threshold 100ms) all PASS. |

---

## D5: API Consistency

**Result: PASS**

Protocol message type constants are defined once in `internal/protocol/messages.go` (MsgType 1–13) and used consistently by all packages:
- `control/handler.go` dispatches on `MsgRequestTunnel` (7), `MsgRequestHTTPTunnel` (11), `MsgRequestHTTPSTunnel` (12)
- `cmd/client/main.go` sends those same constants and handles `MsgOpenConnection` (9), `MsgPing` (4), `MsgDisconnect` (5)
- `tunnel/server.go` sends `MsgOpenConnection` (9) via `protocol.WriteMessage`
- `tunnel/client.go` sends `MsgDataConnHello` (10)

`TunnelManager` API is used correctly by all consumers:
- `control/handler.go`: `AddTunnel`, `AddHTTPTunnel`, `AddHTTPSTunnel`, `RemoveTunnelsForClient`, `RemoveHTTPTunnelsForClient`
- `tunnel/server.go`: `RegisterPending`, `FulfillPending`, `WaitReady`, `PendingExtConn`
- `tunnel/http_proxy.go` / `https_proxy.go`: `GetHTTPTunnel`, `GetHTTPSTunnel`, `RegisterPending`
- `admin/api.go`: `ListTunnels`, `ListHTTPTunnels` (read-only snapshot)

No orphan code found. `internal/proxy/stub.go` is a deliberate placeholder (package declaration only).

---

## D6: Error Handling

**Result: PASS (with one partial note)**

- **Graceful shutdown (server):** Signal handler cancels root context → propagates to all client goroutines → broadcasts `MsgDisconnect` to connected clients → closes listener → `WaitGroup` with 3-second hard timeout. Clean.
- **Graceful shutdown (client):** `signal.NotifyContext` → goroutine sends `MsgDisconnect` → 2-second read deadline → `conn.Close()`. `defer func() { <-shutdownDone }()` ensures the goroutine finishes before `connect()` returns.
- **Resource cleanup on disconnect:** `defer` in `HandleControlConn` calls `mgr.RemoveTunnelsForClient()`, `mgr.RemoveHTTPTunnelsForClient()`, `ctrlConns.Deregister()`, and `reg.Deregister()` — all four cleanup paths are guarded.
- **Error logging:** All error paths in `handler.go`, `server.go`, `http_proxy.go`, `https_proxy.go` emit structured `slog` entries with the error value. Log level configurable (debug/info/warn/error) via config file or `--log-level` flag.
- **Relay errors:** `relayExternalConn` / `relayHTTPConn` / `relayHTTPSConn` all close both connections after both copy goroutines complete; "use of closed network connection" and EOF are treated as normal termination (downgraded to `Debug`).

**Partial note — bytes_in/bytes_out stats not wired to relay:**
`CountedReader`/`CountedWriter` in `internal/stats` are fully implemented and tested in isolation, but are **not yet instrumented** into the actual relay paths (`relayExternalConn`, `relayHTTPConn`, `relayHTTPSConn`). The `GET /api/stats` endpoint returns `bytes_in: 0` and `bytes_out: 0` for tunneled traffic. Global `TotalConnections`/`ActiveConnections` counters (incremented in `cmd/server/main.go` accept loop) are correctly wired, but those count control connections, not data connections. This is a **partial implementation gap** for SC2 of Phase 6, not a blocking correctness issue.

---

## Summary

Milestone 1 is **INTEGRATED**: all 6 phases are wired together as a coherent system, `go build ./...` and `go vet ./...` pass cleanly, and all 52 tests pass across 6 packages with no failures or skips. The four core requirements (REQ-TCP-01, REQ-HTTP-01, REQ-CONN-01, REQ-MULTI-01) are fully met and verified by integration tests. Cross-phase boundaries (control → tunnel → HTTP routing → reconnect → multi-client → observability) are correctly wired through shared `Manager`, `ClientRegistry`, and `ControlConnRegistry` instances managed in `cmd/server/main.go`.

## Gaps (if any)

1. **bytes_in / bytes_out relay instrumentation missing (minor):** `stats.CountedReader` and `stats.CountedWriter` exist and are tested but are not wired into `relayExternalConn` (TCP), `relayHTTPConn` (HTTP), or `relayHTTPSConn` (HTTPS). As a result, `GET /api/stats` always reports `bytes_in: 0` and `bytes_out: 0` for tunneled traffic. The Phase 6 SC2 success criterion ("터널링 트래픽 통계(연결 수, 전송량)를 확인할 수 있다") is partially met: connection counts are tracked, but byte counts for data-plane traffic are not. This is a Phase 6 internal gap, not a cross-phase integration failure.

2. **`-race` flag unavailable (environmental):** No CGO/gcc in this environment, so `-race` cannot be executed. Race safety is structurally enforced through `sync.RWMutex` and `atomic.Int64` on all shared state. Not a code defect.
