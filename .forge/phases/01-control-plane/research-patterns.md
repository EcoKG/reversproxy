# Research: Control Plane Patterns

## Summary
Control plane protocols for reverse proxies typically use a long-lived TCP connection with message framing, client authentication tokens, and heartbeat mechanisms. Length-prefixed binary or JSON-line encoding is standard. Multiplexing (yamux/smux) over a single connection reduces overhead. Graceful disconnect detection requires both TCP keepalive and application-level heartbeats due to OS kernel timing limitations.

## Findings

### [H] Message Framing: Length-Prefixed Binary is Industry Standard
**Pattern:** ngrok uses 64-bit little-endian length prefix before each message (netstring format). frp uses the msg.Message interface with binary serialization. This avoids ambiguity in message boundaries over TCP streams where data arrives in arbitrary chunks.

**Pros:**
- Deterministic parsing: know exact byte count before deserializing
- Compact: 8-byte overhead vs JSON line delimiters
- Binary serialization (protobuf/msgpack) 30-70% smaller than JSON

**Cons:**
- Not human-readable (harder to debug without tooling)
- Requires frame sync logic

**Recommendation for Phase 1:** Use length-prefixed binary with protobuf. Start simple: `[4-byte big-endian length][protobuf message]`. Go's `google.golang.org/protobuf` handles this well. Define 3 core message types:
- ClientRegister (client_id, auth_token, version)
- ServerReady (server_id, timestamp)
- Heartbeat/Ping (sequence_id, timestamp)

### [H] Client Registration Flow Must Be Synchronous Before Proceeding
**Pattern:** ngrok sends Auth message → waits for AuthResp → then sends ReqTunnel. frp uses auth_token in every message with timestamp-based validation (must not exceed 15 minutes skew).

**Critical for Phase 1:**
1. Client connects TCP
2. Client sends ClientRegister (with auth_token or cert)
3. Server validates → sends RegisterResp (accept/reject + server_id)
4. Only after this, client is "registered" (visible in logs as independently identified)
5. Both sides now detect timeouts on this single connection

**Recommendation:** Skip token-based auth initially (use pre-shared secret in config files). Focus on:
- Unique client_id generation (UUID or hostname-based)
- Atomic server-side registration in a map[clientID]*ClientConn
- Log registration events with client metadata

### [H] Heartbeat + TCP Keepalive Combo Required for Reliable Disconnection
**Pattern:** Dead TCP connections take ~11 minutes to detect on Linux with default kernel settings. RabbitMQ uses application-level heartbeat (every 60s) combined with TCP keepalive (15s). This ensures detection within 2 probes.

**Why both are needed:**
- TCP keepalive: handled by kernel, survives silent network partitions
- Heartbeat: application layer can detect and handle gracefully, less overhead

**Recommendation for Phase 1:**
- Enable TCP keepalive immediately: `conn.SetKeepAlive(true)` (Go 1.13+ defaults to 15s idle)
- Add application-level Ping message: server sends every 30s, client responds with Pong
- Timeout logic: if 2 consecutive Ping messages unanswered, close connection
- Both sides log disconnection reason (clean close vs timeout)

### [M] Multiplexing: Defer to Phase 2, Use Dedicated Channels for Phase 1
**Options:**
1. **yamux** (HashiCorp): SPDY-like, balanced latency/throughput, 8+ byte overhead
2. **smux** (xtaci): Minimal overhead (8 bytes), shared buffer pool, good for high-concurrency
3. **Dedicated connections:** One TCP for control plane, separate connections for each tunnel

**Recommendation for Phase 1:** Skip multiplexing. Use single TCP connection for control plane. Reasons:
- Control plane messages (register, heartbeat) are low-frequency and small
- Simplifies debugging and state management
- Multiplexing overhead not justified until many tunnels per client
- Plan to add yamux in Phase 2 once control plane is stable

### [M] Client Identification: Use Unique ID + Metadata for Independent Tracking
**Pattern:** frp supports proxy groups with load balancing. ngrok uses agent tokens. Each client needs:
- UUID or hash-based unique identifier (not hostname alone—multiple clients per host possible)
- Name/label (for user-facing logs)
- Registration timestamp
- Last heartbeat timestamp

**Server state structure (pseudo-code):**
```
type ClientRegistry struct {
  mu sync.RWMutex
  clients map[string]*Client  // key: unique client_id
}

type Client struct {
  ID string
  Name string
  RegisteredAt time.Time
  LastHeartbeatAt time.Time
  Conn net.Conn
}
```

**Recommendation:** Generate client_id on server side (assign on ClientRegister). Return in RegisterResp. Client stores and uses in all subsequent messages. Log entry on register: `"Client registered: id=abc123, name=my-client, addr=10.0.0.1:54321"`

### [M] Graceful Shutdown: Explicit Disconnect Message Required
**Pattern:** Simply closing TCP doesn't guarantee the other side noticed immediately. Applications should send an explicit Disconnect message before closing the socket.

**Flow:**
1. Client sends ClientDisconnect message (optional reason field)
2. Server receives, logs, removes from registry
3. Server sends DisconnectAck (optional)
4. Both sides close socket

**Recommendation for Phase 1:** Implement basic version:
- Add Disconnect message type to protobuf
- Server sends it to all clients on graceful shutdown (SIGTERM handler)
- Client sends it when user terminates
- Timeout: if no DisconnectAck within 2s, force-close anyway

### [M] Security: TLS + Mutual Auth for Control Plane, Token-Based for Initial Auth
**Pattern:** Ghostunnel uses mTLS (certificates on both sides). ngrok uses authtoken + optional IP policies. frp uses auth_token + timestamp.

**For Phase 1 (greenfield project):**
1. Use TLS (one-way, server cert signed by self-signed CA or Let's Encrypt)
2. Client auth: pre-shared token in ClientRegister message (same as frp)
3. Defer mutual mTLS to Phase 2 if certificate distribution is complex

**Quick win:** Self-signed server cert + client validates pinned public key (hardcoded in binary or config file).

**Recommendation:**
- Generate self-signed cert during setup: `go run example_certs.go`
- Client reads cert from file or embeds public key
- Connection: `tls.Dial("tcp", "server:port", &tls.Config{...})`
- Log TLS version + cipher suite on connect

## References
- [ngrok Protocol Design](https://github.com/inconshreveable/ngrok/blob/master/docs/DEVELOPMENT.md)
- [FRP (fatedier)](https://github.com/fatedier/frp)
- [Yamux (HashiCorp)](https://github.com/hashicorp/yamux)
- [Smux (xtaci)](https://github.com/xtaci/smux)
- [Length-Prefix Framing](https://eli.thegreenplace.net/2011/08/02/length-prefix-framing-for-protocol-buffers)
- [RabbitMQ Heartbeats](https://www.rabbitmq.com/docs/heartbeats)
- [Using TCP Keepalive with Go](https://felixge.de/2014/08/26/using-tcp-keepalive-with-go/)
- [Ghostunnel (Go mTLS Proxy)](https://github.com/ghostunnel/ghostunnel)
- [Protobuf in Go](https://pkg.go.dev/google.golang.org/protobuf/proto)
