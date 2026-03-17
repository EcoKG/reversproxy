# HTTP CONNECT Proxy — Implementation Report

**Date:** 2026-03-18
**Task:** Add an HTTP CONNECT proxy to the reversproxy client

---

## Summary

An HTTP CONNECT proxy (`HTTPS_PROXY=http://127.0.0.1:8080`) has been added to the client side of the reversed proxy architecture.  It reuses the exact same tunnel mux (MsgSOCKSConnect / MsgSOCKSData / MsgSOCKSClose) as the existing SOCKS5 proxy — the only difference is the local listener protocol.

---

## Files Changed

### New

| File | Description |
|------|-------------|
| `internal/socks/httpconnect.go` | HTTP CONNECT proxy listener + per-connection handler |
| `internal/socks/httpconnect_test.go` | 3 integration tests for the HTTP CONNECT proxy |

### Modified

| File | Change |
|------|--------|
| `internal/config/config.go` | Added `HTTPProxyAddr string` field (default `:8080`) and updated `DefaultClientConfig()` |
| `cmd/client/main.go` | Added `--http-proxy-addr` flag; passed `httpProxyAddr` through `handleServerConn`; added `StartHTTPConnectProxy` startup block |

---

## Implementation Detail

### `internal/socks/httpconnect.go`

`StartHTTPConnectProxy(ctx, addr, cw, mux, log)` starts a TCP listener and spawns `handleHTTPConnectConn` per connection.  The handler:

1. Reads the HTTP request line (`CONNECT host:port HTTP/1.1`).
2. Drains remaining headers until the blank line.
3. Returns a `200 OK` informational page for non-CONNECT methods (same UX as `oldproxy/server.py`).
4. Parses `host:port` from the CONNECT target.
5. Calls `mux.NewChannel(connID)` **before** sending `MsgSOCKSConnect` (same race-free ordering as SOCKS5 client).
6. Writes `MsgSOCKSConnect{ConnID, host, port}` to the server via `ControlWriter`.
7. Waits for `MsgSOCKSReady` on `ch.ReadyCh` (30 s timeout).
8. On success: writes `HTTP/1.1 200 Connection Established\r\n\r\n`.
9. On failure: writes `HTTP/1.1 502 Bad Gateway\r\n\r\n`.
10. Runs a bidirectional relay identical to `client.go`:
    - Goroutine A reads from `conn` (via `bufio.Reader` to recover any buffered header bytes) → pushes to `outSend` channel.
    - Goroutine B reads from `ch.Recv` (MsgSOCKSData from server) → writes to `conn`.
    - Mux writer drains `outSend` → `MsgSOCKSData` frames.
    - After `outSend` is closed: sends `MsgSOCKSClose`, waits for goroutine B.

The `bufio.Reader` wrapping is important: `http.ReadResponse` / manual header reading may buffer bytes past the blank line; all subsequent relay reads must drain that buffer first.

### Config & Flag

```
--http-proxy-addr  string   local HTTP CONNECT proxy address (default ":8080")
```

YAML key: `http_proxy_addr`.  Setting to `""` disables the listener.

### Startup in `cmd/client/main.go`

```go
if httpProxyAddr != "" {
    httpCtx, httpCancel := context.WithCancel(ctx)
    defer httpCancel()
    if err := socks.StartHTTPConnectProxy(httpCtx, httpProxyAddr, sharedWriter, clientMux, log); err != nil {
        log.Error("failed to start HTTP CONNECT proxy", ...)
    } else {
        fmt.Printf("HTTP CONNECT proxy: http://%s (use HTTPS_PROXY=http://%s)\n", ...)
    }
}
```

Both SOCKS5 and HTTP CONNECT share the same `sharedWriter` (serialised writes to the control connection) and `clientMux` (inbound frame dispatch).

---

## Tests

`internal/socks/httpconnect_test.go` — 3 tests, all passing:

| Test | Verifies |
|------|----------|
| `TestHTTPConnect_BasicRelay` | Full CONNECT handshake + echo relay (write + CloseWrite + read) |
| `TestHTTPConnect_NonConnectMethod` | GET request gets a 200 informational page, not a tunnel |
| `TestHTTPConnect_TargetUnreachable` | CONNECT to closed port → 502 Bad Gateway |

Tests reuse `runFakeServerWithClientMux` and `startEchoServer` from `client_test.go` (same `socks_test` package).

---

## Build & Test Results

```
go build ./...   → OK (no errors)
go test ./...    → all packages PASS
```

```
ok  github.com/EcoKG/reversproxy/internal/socks     0.289s
ok  github.com/EcoKG/reversproxy/internal/config    (cached)
ok  github.com/EcoKG/reversproxy/internal/tunnel    (cached)
ok  github.com/EcoKG/reversproxy/internal/control   (cached)
... (all other packages pass)
```

---

## Usage

On the client (closed-network machine):

```bash
# Start the client — HTTP CONNECT proxy starts automatically on :8080
./client --config config.yaml

# In another shell or tool:
export HTTPS_PROXY=http://127.0.0.1:8080
claude
```

To use a non-default port:

```bash
./client --http-proxy-addr :9090
# or in config.yaml:
http_proxy_addr: ":9090"

# Disable entirely:
http_proxy_addr: ""
```
