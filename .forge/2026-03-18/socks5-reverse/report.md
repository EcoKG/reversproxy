# SOCKS5 Reverse Proxy — Implementation Report

**Date:** 2026-03-18
**Task:** Reverse the SOCKS5 proxy direction (listener moves from server to client)

---

## Objective

The original architecture placed the SOCKS5 listener on the server:

```
Browser (SOCKS5) → Server:1080 → MsgSOCKSConnect → Client → dial internet → data port back to server
```

This broke when the client was on a closed network (no inbound connectivity). The fix moves the listener to the client and multiplexes all data through the existing control TCP connection:

```
SOCKS5 user → Client:1080 → MsgSOCKSConnect → Server → dial internet
                           ← MsgSOCKSReady   ←
                           ↔ MsgSOCKSData    ↔  (bidirectional relay)
                           ↔ MsgSOCKSClose   ↔  (half-close signal)
```

---

## Files Changed

### New files

| File | Purpose |
|------|---------|
| `internal/tunnel/socks_mux.go` | In-process multiplexer: maps ConnID → `SOCKSChannel` (io.Pipe recv, ReadyCh, done) |
| `internal/socks/client.go` | Client-side SOCKS5 listener + per-connection handler |
| `internal/socks/client_test.go` | Integration tests: NoAuth, Auth, TargetUnreachable |

### Modified files

| File | Change |
|------|--------|
| `internal/protocol/messages.go` | Added `MsgSOCKSData = 16`, `MsgSOCKSClose = 17`; added `SOCKSData`, `SOCKSClose` structs; updated direction comments |
| `internal/control/handler.go` | Replaced old MsgSOCKSReady handler with three new cases (Connect/Data/Close); added `serverCtrlWriter` mutex; added `handleServerSOCKSConnect` |
| `internal/config/config.go` | Moved `SOCKSAddr/User/Pass` from `ServerConfig` to `ClientConfig` |
| `cmd/server/main.go` | Removed socks import and startup block |
| `cmd/client/main.go` | Added `--socks-addr/user/pass` flags; added `clientConnWriter` mutex; wired mux + message dispatcher + `StartClientSOCKSProxy` |
| `internal/socks/server.go` | Added deprecation comment (kept for reference) |

---

## Design Decisions

### Control connection multiplexing

All SOCKS data travels through the single persistent control TCP connection. Each logical channel is identified by a UUID (`ConnID`). This requires no additional inbound ports on the client, which is the entire point of the reversal.

### SOCKSMux (internal/tunnel/socks_mux.go)

Each `SOCKSChannel` holds:
- `ReadyCh chan SOCKSReadyResult` — signals dial success/failure from server
- `Recv io.Reader` / `recvW io.WriteCloser` — `io.Pipe()` pair; mux delivers data by writing to `recvW`, relay goroutine reads from `Recv`
- `done chan struct{}` — closed by `Remove`/`DeliverClose` for select-based abort
- `sync.Once` — ensures teardown is idempotent

### Half-close sequence

TCP half-close semantics are preserved across the multiplexed channel:

1. Client finishes writing → sends `MsgSOCKSClose`
2. Server reads EOF from client channel → calls `CloseWrite()` on target TCP conn
3. Target sends remaining data + FIN
4. Server reads FIN → drains `outSend` → sends `MsgSOCKSClose` to client
5. Client dispatcher calls `DeliverClose` → closes `recvW` → relay goroutine B gets EOF
6. Both sides fully closed

### Serialised control writes

Multiple goroutines per connection (mux writer + potential concurrent channels) write to the shared control connection. Both `clientConnWriter` (cmd/client) and `serverCtrlWriter` (control/handler) wrap the conn with `sync.Mutex`. The test's `pipeControlWriter` does the same.

### `range outSend` pattern

The mux writer goroutine uses `for payload := range outSend` rather than `select { case outSend: / case done: }`. Closing `outSend` naturally drains all buffered payloads before the goroutine exits, eliminating the data-loss race where `done` could be selected before all data was sent.

---

## Bugs Fixed During Development

| Bug | Root cause | Fix |
|-----|-----------|-----|
| Test timeout (30 s) on `ch.ReadyCh` | Client-side message dispatcher goroutine was missing in test; nobody called `DeliverReady` | Added explicit dispatcher goroutine in `newClientTestEnv` |
| `serverMux Deliver failed: unknown connID` | `runFakeServerChannel` created a local mux instead of using the shared one | Pass shared `serverMux`; retrieve pre-registered channel via `Get` |
| `MsgSOCKSData` before channel registered | Race: dispatch goroutine handed off to `go runFakeServerChannel` before registration | Pre-register channel synchronously in dispatch loop before spawning goroutine |
| Relay deadlock (goroutine B blocked forever) | `ch.Close()` only closed `done` channel, not `recvW` | Call `mux.Remove(connID)` explicitly, which closes `recvW` via `sync.Once` |
| Data loss (`MsgSOCKSClose` sent before `MsgSOCKSData`) | `select` between `outSend` and `done` has non-deterministic choice | Replace `select` with `range outSend`; close channel to signal stop |
| Echo arriving after connection closed | After sending `MsgSOCKSClose`, called `mux.Remove` immediately | Half-close: send `MsgSOCKSClose`, then `<-recvDone` before `mux.Remove` |
| Frame corruption (concurrent writes) | Multiple goroutines writing to `pipeControlWriter` without a lock | Added `sync.Mutex` to `pipeControlWriter`; same pattern in production writers |

---

## Test Results

```
ok  github.com/EcoKG/reversproxy/internal/socks    0.192s
ok  github.com/EcoKG/reversproxy/internal/tunnel   2.428s
ok  github.com/EcoKG/reversproxy/internal/control  0.761s
ok  github.com/EcoKG/reversproxy/internal/protocol 0.003s
ok  github.com/EcoKG/reversproxy/internal/config   0.005s
ok  github.com/EcoKG/reversproxy/internal/admin    0.009s
ok  github.com/EcoKG/reversproxy/internal/reconnect 0.306s
```

All packages pass. Three new tests added:

- `TestClientSOCKS5_NoAuth` — end-to-end echo with no auth
- `TestClientSOCKS5_Auth` — end-to-end echo with RFC 1929 username/password auth
- `TestClientSOCKS5_TargetUnreachable` — verifies CONNECT failure returns non-zero reply byte

---

## Usage

**Client** (closed network machine):

```sh
./client --server-addr=<server>:9000 \
         --socks-addr=127.0.0.1:1080 \
         --socks-user=alice \
         --socks-pass=secret
```

**Server** (internet-facing machine):

```sh
./server --listen=:9000
# No --socks-addr needed; server handles SOCKS internally via control channel
```

Configure browser/tool to use SOCKS5 proxy at `127.0.0.1:1080`.
