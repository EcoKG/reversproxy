# Report: Connection Direction Refactor

**Task ID**: conn-direction-refactor-1400
**Date**: 2026-03-17
**Verdict**: PASS — all tests green

---

## Summary

Reversed the TCP connection initiation direction between server and client:

- **Before**: Server called `tls.Listen`, client called `tls.Dial` to reach the server.
- **After**: Client calls `tls.Listen` and waits; server calls `tls.Dial` to each configured client address.

This enables clients behind NAT/firewalls to be reachable — the server actively dials out, and clients only need an inbound-listening port on themselves.

---

## Files Modified

### Core Logic

| File | Change |
|------|--------|
| `internal/config/config.go` | Removed `ServerConfig.Addr`; added `ServerConfig.Clients []ClientTarget`; new `ClientTarget{Name, Address, AuthToken}`; renamed `ClientConfig.ServerAddr` → `ListenAddr`; added `CertPath`/`KeyPath` to `ClientConfig` |
| `internal/control/handler.go` | `HandleControlConn` now runs on the server side after dialing: sends `MsgClientRegister`, reads `MsgRegisterResp`. Client name carried in `RegisterResp.ServerID`. |
| `cmd/server/main.go` | Removed accept loop; added `dialClientLoop` with exponential backoff per `cfg.Clients` entry; one goroutine per target, joined with `sync.WaitGroup` |
| `cmd/client/main.go` | Removed `tls.Dial` + reconnect loop; added `tls.Listen` on `cfg.ListenAddr` with cert from `LoadOrGenerateCert`; accept loop per server connection; reversed handshake in `handleServerConn` |

### Tests

| File | Change |
|------|--------|
| `internal/config/config_test.go` | Updated to use `ListenAddr`, `Clients`, `ClientTarget` |
| `internal/control/integration_test.go` | Full rewrite: `startTestClient` (TLS listener), `startTestServer` (dials client), reversed handshake flow |
| `internal/tunnel/tunnel_integration_test.go` | Full rewrite: `startInfra` returns `dialFn`; `connectClient` starts one-shot TLS listener |
| `internal/tunnel/http_routing_test.go` | Full rewrite: `startHTTPInfra` returns `dialFn`; `acceptServerConn` helper |
| `internal/tunnel/multi_client_test.go` | Full rewrite: `mc_connectTCP` uses `dialFn` and one-shot TLS listener |
| `internal/reconnect/integration_test.go` | Full rewrite: `clientListener` (TLS listener struct), `restartableServer` (dials via `HandleControlConn`); sequential RequestTunnel/TunnelResp read with Ping drain to eliminate read-race |

### Files NOT Modified

Protocol layer (`messages.go`, `framing.go`), tunnel relay, HTTP/HTTPS proxy, admin API, stats, registry, heartbeat, TLS helpers, backoff algorithm — all transport-agnostic and unchanged.

---

## Key Design Decisions

1. **`HandleControlConn` repurposed as server-side dialer**: The function previously accepted an incoming conn; now it dials out. The handshake direction flipped: server sends `ClientRegister`, client validates and sends `RegisterResp`. This preserves all downstream message handling (RequestTunnel, Ping/Pong, Disconnect) unchanged.

2. **`RegisterResp.ServerID` carries client name**: Rather than adding a new field, the existing unused `ServerID` field in `RegisterResp` is reused to carry the client's chosen name back to the server.

3. **Reconnect logic moves to server**: `reconnect.Backoff` is now used in `cmd/server/main.go`'s `dialClientLoop`. The client no longer needs reconnect — it listens permanently.

4. **`tls.Listener` is not exported**: `tls.Listen` returns `net.Listener`. Timeout on accept is implemented via a goroutine+channel pattern with `time.AfterFunc` closing the listener, instead of a type assertion to `*tls.Listener`.

5. **Ping/TunnelResp read race fixed**: In reconnect SC2 test, sending `RequestTunnel` and reading the response must not compete with a Ping-handler goroutine on the same conn. Fixed by doing the RequestTunnel → TunnelResp exchange sequentially (draining Pings inline) before handing the conn to the message loop goroutine.

---

## Test Results

```
ok  github.com/starlyn/reversproxy/internal/admin       (cached)
ok  github.com/starlyn/reversproxy/internal/config      (cached)
ok  github.com/starlyn/reversproxy/internal/control     (cached)
ok  github.com/starlyn/reversproxy/internal/protocol    (cached)
ok  github.com/starlyn/reversproxy/internal/reconnect   0.304s
ok  github.com/starlyn/reversproxy/internal/tunnel      (cached)
```

All 6 packages pass. No failures.

### Reconnect package (previously failing):

- `TestSC1_AutoReconnectAfterServerRestart` — PASS (0.10s)
- `TestSC2_TunnelRestoredAfterReconnect` — PASS (0.20s) *(was timing out at 60s)*
- `TestSC3_ExponentialBackoff` — PASS (0.00s)

---

## Root Cause of Final Bug (SC2 timeout)

The SC2 test sent `RequestTunnel` on `clientConn1` and then called `protocol.ReadMessage(clientConn1)` expecting `MsgTunnelResp`. However, a goroutine started in `acceptOne` was also calling `protocol.ReadMessage` on the same conn to handle `MsgPing`. The server's heartbeat goroutine sent a `Ping` (type=3) before the `TunnelResp` arrived; whichever goroutine won the read race consumed it. When the test won, it got `type=3` instead of `MsgTunnelResp (type=8)` and failed with `"type=3"`.

Fix: removed the concurrent Ping-handler goroutine from `acceptOne`. The RequestTunnel/TunnelResp exchange is now done entirely in the main test goroutine, draining any interleaved Pings in a local loop before handing the conn to the background `runMsgLoop`.
