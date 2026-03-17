# Phase 2 Report: TCP 터널링

## Verdict: PASS

---

## Files Created/Modified

| File | Action | Description |
|------|--------|-------------|
| `internal/protocol/messages.go` | Modified | Added `MsgRequestTunnel`, `MsgTunnelResp`, `MsgOpenConnection`, `MsgDataConnHello` constants (values 7–10) and corresponding structs: `RequestTunnel`, `TunnelResp` (with `ServerDataAddr`), `OpenConnection`, `DataConnHello` |
| `internal/tunnel/manager.go` | Created | Thread-safe `Manager` tracking tunnels per client and pending data connections. Provides `AddTunnel`, `RemoveTunnelsForClient`, `RegisterPending`, `FulfillPending`, `WaitReady`, `PendingExtConn` |
| `internal/tunnel/server.go` | Created | `StartDataListener` (accepts client data connections, reads `DataConnHello`, fulfils pending); `StartPublicListener` (accepts external connections, sends `OpenConnection` over control conn, relays via `io.Copy`) |
| `internal/tunnel/client.go` | Created | `HandleOpenConnection` — on receiving `OpenConnection`: dials server data port, sends `DataConnHello`, dials local service, bidirectional relay with `io.Copy` |
| `internal/control/handler.go` | Modified | Signature extended with `mgr *tunnel.Manager` and `dataAddr string`; added `MsgRequestTunnel` case calling `handleRequestTunnel`; cleanup calls `mgr.RemoveTunnelsForClient` on disconnect |
| `cmd/server/main.go` | Modified | Added `-data-addr` flag; initialises `tunnel.Manager`; calls `tunnel.StartDataListener`; passes `mgr` and `resolvedDataAddr` to `HandleControlConn` |
| `cmd/client/main.go` | Modified | Added `-local-host`, `-local-port`, `-pub-port` flags; sends `MsgRequestTunnel` when `local-port > 0`; handles `MsgOpenConnection` in message loop via `tunnel.HandleOpenConnection` |
| `internal/control/integration_test.go` | Modified | Updated `HandleControlConn` call to match new signature (passes `nil, ""` for mgr/dataAddr) |
| `internal/tunnel/tunnel_integration_test.go` | Created | Integration tests for all 3 success criteria using in-process TLS server + echo service |

---

## Architecture

```
External User
     │  TCP
     ▼
Server public listener (tunnel.StartPublicListener)
     │  sends MsgOpenConnection over control TLS conn
     ▼
Client message loop (cmd/client/main.go)
     │  calls tunnel.HandleOpenConnection
     ▼
Client dials server data port → sends DataConnHello → matched to external conn
     │  io.Copy both directions
     ▼
Client dials local service
     │  io.Copy both directions
     ▼
Local Service (e.g. :8080)
```

Control connection and data connections are **fully separate TCP connections**.
Each external connection creates its own data connection pair. No multiplexing.

---

## Tests

```
ok  github.com/starlyn/reversproxy/internal/control   (all Phase 1 tests still pass)
ok  github.com/starlyn/reversproxy/internal/protocol  (all framing tests still pass)
ok  github.com/starlyn/reversproxy/internal/tunnel    0.203s
  --- PASS: TestSC1_ExternalConnectionReachesLocalService (0.05s)
  --- PASS: TestSC2_DataIntegrity (0.05s)
  --- PASS: TestSC3_ConcurrentConnections (0.08s)
```

---

## Success Criteria

- **SC1: PASS** — External user connected to server's public port and exchanged data with a local echo service through the tunnel. Message `"hello via tunnel"` was transmitted and echoed without error.

- **SC2: PASS** — A 64 KB binary payload (pattern: `byte(i % 251)`) was sent through the tunnel and compared byte-by-byte. Zero corruption or loss across 65,536 bytes in both directions.

- **SC3: PASS** — 5 concurrent external connections to the same tunnel public port all completed successfully. Each connection received its unique echo message; 5 distinct connIDs were created and relayed simultaneously.
