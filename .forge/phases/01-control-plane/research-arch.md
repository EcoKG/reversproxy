# Research: Architecture

## Summary
Control plane must remain independent from data plane for resilience. Client initiates persistent control connection (TCP or WebSocket) with heartbeat mechanism. Protocol requires explicit message framing with multiplexing support. Go's goroutine-per-connection model with io.Copy handles high concurrency efficiently. Connection pools and health checking detect failures early.

## Findings

### [H] Control & Data Plane Separation is Non-Negotiable
Control plane (client registration, tunnel requests, auth) must be decoupled from data plane (actual traffic relay). If control connection fails, existing data tunnels should remain active. Conversely, data plane failures should not block control messaging. This separation enables independent scaling and security boundaries.

**Reference:** [Why Decoupling Control and Data Planes Is the Future of SaaS](https://thenewstack.io/why-decoupling-control-and-data-planes-is-the-future-of-saas/)

### [H] Persistent Control Connection with Heartbeat
Client establishes one persistent control connection (TCP or WebSocket multiplexed). Frp uses default 10s heartbeat interval with 90s timeout. Set explicit read/write deadlines per operation; reset after success. Without deadlines, idle goroutines leak.

**Go Pattern:** Use `conn.SetDeadline()` or `context.WithTimeout()`. Defer cancel() to release resources. Never set deadlines once then forget.

**Reference:** [How to Use Context for Request Cancellation in Go](https://oneuptime.com/blog/post/2026-01-30-go-context-request-cancellation/view)

### [H] Protocol: Explicit Message Framing Required
Control messages must have clear boundaries (length-prefixed or delimiter-based). Frp supports TCP multiplexing (single connection carries multiple logical channels via message framing). Design message schema early: include message type, sequence ID, payload length, optional correlation ID for request/response pairs.

**Why:** Streaming protocols without framing cause ambiguity when multiple logical messages arrive in one TCP segment.

### [M] Tunnel Setup Flow: Client Handshake → Registration → Ready
1. Client connects to proxy server (control connection)
2. Client sends auth token + client ID
3. Server validates, sends ACK with server config
4. Client enters ready state (can accept tunnel requests)
5. Server sends `RequestTunnel(tunnel_id, service_name)` to trigger client
6. Client connects to local service, creates data tunnel back to proxy
7. Proxy routes external traffic through data tunnel

**Diagram:** External → Proxy:443 → (finds tunnel pool) → pick connection → Client → LocalService:8080

### [M] Connection Pool & Round-Robin
Server maintains pool per tunnel (one control conn can have N data connections). Use round-robin or load-aware selection. Remove failed connections immediately on error. Health checks: optional but valuable (ping data connections periodically or check on-use).

**Reference:** [Reverse Proxy Tunnel - GOST](https://gost.run/en/tutorials/reverse-proxy-tunnel/)

### [M] Go Goroutine-per-Connection + io.Copy Pattern
Spawn one goroutine per control connection; one goroutine pair (read + write) per data tunnel. Use `io.Copy(src, dst)` for relay—it handles buffering efficiently. Cost: ~2KB per goroutine. At 10K concurrent connections, ~20MB total memory for goroutines (acceptable).

**Code skeleton:**
```go
for {
  conn, _ := listener.Accept()
  go handleControlConn(conn)
}

func handleControlConn(conn net.Conn) {
  defer conn.Close()
  for {
    msg := readMessage(conn)  // blocking read
    // process msg, maybe spawn tunnel relay
  }
}

func relayTunnel(remote, local net.Conn) {
  go io.Copy(remote, local)  // remote ← local
  go io.Copy(local, remote)  // local ← remote
}
```

**Reference:** [How to Build High-Performance Network Services in Go](https://oneuptime.com/blog/post/2026-02-01-go-high-performance-network-services/view)

### [M] Multiplexing: TCP Mux + Message Framing
If single connection carries multiple tunnels, use message framing. Frp achieves this via TCP stream multiplexing (similar to HTTP/2). Alternative: spawn separate data connection per tunnel (simpler but more overhead).

**Decision:** Phase 1 can start with separate connections; add multiplexing in Phase 2 if concurrency limits warrant it.

### [L] Keepalive vs Heartbeat
- **Keepalive (TCP level):** OS-level probes; kernel-managed; survives app restart (sometimes).
- **Heartbeat (app level):** Explicit message; app detects stale connections; more reliable for detecting logical failures.

Use both: TCP keepalive for firewall/NAT traversal, app heartbeat for detecting proxy crashes or hangs.

### [L] WebSocket Option for NAT Traversal
Standard TCP may fail behind aggressive NAT/firewalls. WebSocket over TLS (wss://) can tunnel through HTTP proxies. Adds overhead but increases compatibility. Consider as fallback transport, not primary.

## Design Decisions for Phase 1

1. **Control Protocol:** Length-prefixed message framing (simple, reliable). Binary encoding (protobuf or msgpack) for efficiency.
2. **Connection Model:** One control conn per client; separate data connections per tunnel. Upgrade to multiplexing if needed.
3. **Auth:** Token-based (pre-shared key or TLS cert pinning). Validate on control handshake.
4. **Heartbeat:** 10s interval, 30s timeout (tunable). App-level ping/pong.
5. **Go Patterns:** Goroutine-per-connection, context for cancellation, io.Copy for relay, explicit deadlines.

## References
- [High-Performance Network Services in Go (2026)](https://oneuptime.com/blog/post/2026-02-01-go-high-performance-network-services/view)
- [Mastering Go's Network I/O](https://dev.to/jones_charles_ad50858dbc0/mastering-gos-network-io-build-scalable-high-performance-apps-579o)
- [Go Context & Cancellation Patterns](https://go.dev/blog/context)
- [FRP GitHub](https://github.com/fatedier/frp)
- [Awesome Tunneling Projects](https://github.com/anderspitman/awesome-tunneling)
- [Reverse Proxy Tunnel - GOST](https://gost.run/en/tutorials/reverse-proxy-tunnel/)
