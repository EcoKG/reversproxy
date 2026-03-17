# SOCKS5 Proxy Feature — Implementation Report

**Date:** 2026-03-18
**Status:** Complete
**Build:** `go build ./...` — PASS
**Tests:** `go test ./...` — all PASS

---

## Summary

Added SOCKS5 proxy support to the reversproxy project. External users connect to the server's SOCKS5 listener, the server performs the RFC 1928 / RFC 1929 handshake, then routes the CONNECT request through the selected client tunnel. The client performs DNS resolution and the actual TCP dial; data is relayed bidirectionally through the existing data connection infrastructure.

Architecture:

```
Browser (SOCKS5) → Server:1080 → control channel (MsgSOCKSConnect)
  → Client → net.Dial(target) → Internet
  → Client → data conn back to server → relay
```

---

## Files Changed

### New Files

| File | Purpose |
|------|---------|
| `internal/socks/server.go` | SOCKS5 proxy listener (RFC 1928 + RFC 1929) |
| `internal/socks/server_test.go` | Integration tests (5 test cases) |

### Modified Files

| File | Change |
|------|--------|
| `internal/protocol/messages.go` | Added `MsgSOCKSConnect` (type 14), `MsgSOCKSReady` (type 15), `SOCKSConnect` struct, `SOCKSReady` struct |
| `internal/tunnel/manager.go` | Added `pendingSOCKS` type, `pendingSocks` map, `RegisterPendingSOCKS`, `FulfillSOCKS`, `WaitSOCKSReady` |
| `internal/tunnel/ctrl_registry.go` | Added `PickAny()` — returns any connected client control conn |
| `internal/tunnel/client.go` | Added `HandleSOCKSConnect`, `handleSOCKSConnectAsync` |
| `internal/control/handler.go` | Added `MsgSOCKSReady` case in message loop; added `handleSOCKSReady` function |
| `internal/config/config.go` | Added `SOCKSAddr`, `SOCKSUser`, `SOCKSPass` fields to `ServerConfig`; default `SOCKSAddr = ":1080"` |
| `cmd/server/main.go` | Added `--socks-addr`, `--socks-user`, `--socks-pass` flags; added SOCKS proxy startup; imports `internal/socks` |
| `cmd/client/main.go` | Added `MsgSOCKSConnect` case in message loop; tracks `serverDataAddr` for SOCKS use |

---

## Protocol Design

Two new message types were added after the existing Phase 3 messages:

```
MsgSOCKSConnect  (type 14) — server → client
  ConnID     string   // unique connection ID
  TargetHost string   // hostname or IP (DNS resolved on client side)
  TargetPort int      // destination port

MsgSOCKSReady    (type 15) — client → server
  ConnID   string   // matches the SOCKSConnect.ConnID
  Success  bool     // true if dial succeeded
  Error    string   // human-readable failure reason (empty on success)
```

---

## SOCKS5 Implementation (RFC 1928 / RFC 1929)

`internal/socks/server.go` implements the full SOCKS5 handshake:

1. **Greeting** — reads `[VER, NMETHODS, METHODS...]`, selects `NO_AUTH` (0x00) or `USERNAME/PASSWORD` (0x02) based on config.
2. **Auth** (RFC 1929) — if auth is required, reads `[0x01, ULEN, UNAME, PLEN, PASSWD]` and validates; replies `[0x01, 0x00]` (success) or `[0x01, 0x01]` (failure).
3. **CONNECT request** — reads `[VER, CMD=0x01, RSV, ATYP, ADDR, PORT]`; supports IPv4 (ATYP 0x01), domain (ATYP 0x03), IPv6 (ATYP 0x04).
4. **Tunnel routing** — picks a client from `ControlConnRegistry`, registers two pending slots (`pendingSOCKS` for the ready signal, `pendingConn` for the data connection), sends `MsgSOCKSConnect` to the client.
5. **Wait for ready** — blocks on `WaitSOCKSReady` (up to 30 s); fails with REP=0x05 if the client's dial failed.
6. **Wait for data conn** — blocks on `WaitReady` (up to 15 s) for the client's data connection via the existing data listener.
7. **Success reply** — sends `[0x05, 0x00, 0x00, 0x01, 0.0.0.0, 0]` to the SOCKS5 client.
8. **Bidirectional relay** — forwards data between the SOCKS5 client and the client data connection.

DNS resolution happens on the **client side**: the domain name is forwarded verbatim in `SOCKSConnect.TargetHost` and the client calls `net.Dial("tcp", "host:port")`.

---

## Client-Side Handler (`internal/tunnel/client.go`)

`HandleSOCKSConnect` runs asynchronously (goroutine) and:
1. Dials `TargetHost:TargetPort` — DNS resolution here.
2. Dials the server data address, sends `DataConnHello{ConnID}`.
3. Sends `MsgSOCKSReady{ConnID, Success, Error}` via the control connection.
4. If successful, relays data bidirectionally between the target and the data connection.

On any failure before step 3, a `MsgSOCKSReady{Success: false}` is sent so the SOCKS server can unblock and reply with a failure to the SOCKS5 client.

---

## Configuration

```yaml
# server config (config.yaml)
socks_addr: ":1080"    # empty string = disabled
socks_user: ""         # optional; both user and pass must be set to enable auth
socks_pass: ""
```

CLI flags:
```
--socks-addr   SOCKS5 proxy listen address (default :1080; empty = disabled)
--socks-user   SOCKS5 username (empty = no auth)
--socks-pass   SOCKS5 password (empty = no auth)
```

---

## Test Coverage

Five integration tests in `internal/socks/server_test.go`:

| Test | Description |
|------|-------------|
| `TestSOCKS5_NoAuth` | No-auth path: CONNECT to local echo server, verify data integrity |
| `TestSOCKS5_Auth` | RFC 1929 auth path: correct credentials, CONNECT, echo round-trip |
| `TestSOCKS5_WrongAuth` | Wrong password is rejected (status != 0x00) |
| `TestSOCKS5_NoClientConnected` | No client registered → server returns failure reply |
| `TestSOCKS5_WithRealClientHandler` | Full flow using real `tunnel.HandleSOCKSConnect` implementation |

All tests use in-process `net.Pipe()` for the control channel; the data channel uses real TCP (`127.0.0.1:0`).

---

## Key Design Decisions

1. **Two-phase pending**: `pendingSOCKS` waits for the `MsgSOCKSReady` control signal; `pendingConn` (existing infrastructure) waits for the actual data TCP connection. This keeps the data path identical to the existing tunnel relay code.

2. **Client selection**: `ControlConnRegistry.PickAny()` returns the first connected client. Future work could add per-client selection (e.g., round-robin or configurable).

3. **No external libraries**: SOCKS5 is implemented with pure Go `io`/`net`/`encoding/binary`.

4. **DNS on client**: The target hostname is never resolved on the server. The domain string is passed verbatim via `SOCKSConnect.TargetHost`, and `net.Dial` on the client performs the lookup.

---

## Build & Test Results

```
go build ./...   → (no output, success)
go test ./...    → all packages PASS

ok  github.com/EcoKG/reversproxy/internal/admin
ok  github.com/EcoKG/reversproxy/internal/config
ok  github.com/EcoKG/reversproxy/internal/control
ok  github.com/EcoKG/reversproxy/internal/protocol
ok  github.com/EcoKG/reversproxy/internal/reconnect
ok  github.com/EcoKG/reversproxy/internal/socks      ← 5 new tests
ok  github.com/EcoKG/reversproxy/internal/tunnel
```
