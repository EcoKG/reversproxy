# Phase 2 Verification Results

## Build

```
$ go build ./...
(no output — clean build)
```

## Test Run

```
$ go test ./...
?   	github.com/starlyn/reversproxy/cmd/client	[no test files]
?   	github.com/starlyn/reversproxy/cmd/server	[no test files]
?   	github.com/starlyn/reversproxy/internal/logger	[no test files]
ok  	github.com/starlyn/reversproxy/internal/control	(cached)
?   	github.com/starlyn/reversproxy/internal/proxy	[no test files]
ok  	github.com/starlyn/reversproxy/internal/protocol	(cached)
ok  	github.com/starlyn/reversproxy/internal/tunnel	0.203s
```

## Tunnel Integration Test Detail (verbose)

All three tests passed in under 100ms each:

- `TestSC1_ExternalConnectionReachesLocalService` — PASS (0.05s)
- `TestSC2_DataIntegrity` — PASS (0.05s)
- `TestSC3_ConcurrentConnections` — PASS (0.08s)

### SC1 Evidence
An echo server was started on a random local port. The client requested a tunnel.
An external connection was dialed to the server's public port. The message
`"hello via tunnel"` was written, and the same bytes were received back,
proving end-to-end connectivity.

### SC2 Evidence
A 64 KB payload was sent through the tunnel to the echo server and compared
byte-by-byte (using a non-trivial pattern: `byte(i % 251)`). No corruption or
loss was detected at any of the 65,536 bytes.

### SC3 Evidence
5 goroutines simultaneously dialed the same public port. Each sent a unique
message and verified the echo. All 5 completed successfully with distinct
connIDs tracked in the manager. Logs confirmed 5 separate relay pairs
started and finished concurrently.
