# Task 1-9 Summary — Server Main (cmd/server/main.go)

## Status: DONE

## What was implemented

**File:** `cmd/server/main.go` (package main)

### Flags
| Flag      | Default       | Description                  |
|-----------|---------------|------------------------------|
| `-addr`   | `:8443`       | TLS listen address           |
| `-token`  | `changeme`    | Pre-shared auth token        |
| `-cert`   | `server.crt`  | TLS certificate file path    |
| `-key`    | `server.key`  | TLS private key file path    |

### Initialization order
1. `flag.Parse()` — parse all flags
2. `logger.New("server")` — structured JSON logger
3. `control.LoadOrGenerateCert(*certFile, *keyFile)` — load or generate self-signed TLS cert; `os.Exit(1)` on error
4. `control.NewServerTLSConfig(cert)` — TLS 1.3 server config
5. `tls.Listen("tcp", *addr, tlsCfg)` — create TLS listener; `os.Exit(1)` on error
6. `control.NewClientRegistry()` — empty thread-safe registry
7. `context.WithCancel(context.Background())` — root context

### Graceful shutdown
- `signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)` listens on a dedicated goroutine
- On signal: logs `"shutting down"`, calls `cancel()`, closes listener
- Broadcasts `Disconnect{Reason:"server shutdown"}` to all clients in `reg.List()`

### Accept loop
- `for { ln.Accept() ... }` with a `net.Conn`-typed goroutine variable to satisfy `control.HandleControlConn` signature
- Each accepted connection: `wg.Add(1)`, `go func(c net.Conn){ defer wg.Done(); control.HandleControlConn(ctx, c, reg, *token, log) }(conn)`
- Loop breaks on Accept error; distinguishes shutdown vs. unexpected errors via `ctx.Done()` select

### Shutdown drain
- `wg.Wait()` with a 3-second `time.After` hard deadline
- `os.Exit(0)` on clean or timeout exit

## Acceptance Criteria Results

| Criterion | Result |
|-----------|--------|
| `go build ./cmd/server/` | PASS |
| `grep "tls.Listen"` | PASS |
| `grep "HandleControlConn"` | PASS |
| `grep "NewClientRegistry"` | PASS |
| `grep "SIGTERM\|signal.Notify"` | PASS |

## Notes

- First draft had a broken goroutine lambda that attempted to inline-type-assert `net.Conn` from a bare `interface{}` — fixed by typing the lambda parameter directly as `net.Conn` (correct since `tls.Listen` already returns `net.Conn` from `Accept`).
- No circular imports: `cmd/server` imports `internal/control`, `internal/logger`, `internal/protocol`, `net`, `crypto/tls`, `flag`, `os`, `sync`, `syscall`, `time`, `context`.
