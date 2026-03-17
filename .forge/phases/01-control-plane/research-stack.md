# Research: Stack & Project Setup

## Summary
Go 1.22 with modules provides a solid foundation for high-performance networking. Standard library covers all needs (net, crypto/tls, encoding/gob, slog). Project uses cmd/, internal/ structure with two binaries (server, client). Minimal third-party dependencies recommended. Build via Makefile. Control plane will use TLS + binary gob protocol for type-safe, zero-copy communication.

## Findings

### [H] Go Module Path Convention
- **Format**: `github.com/username/project` (module path in go.mod)
- **Go 1.22 Lock**: Project standard, critical for reproducible builds
- **go.sum**: Required for dependency integrity verification
- **Action**: `go mod init github.com/starlyn/reversproxy` when initializing project

### [H] Project Directory Structure
Standard Go layout aligns with [golang-standards](https://github.com/golang-standards/project-layout):
```
cmd/server/main.go     # Proxy server (listen for clients)
cmd/client/main.go     # Client binary (initiate control plane)
internal/control/      # Control plane logic (not exported)
internal/protocol/     # Message protocol definitions
internal/logger/       # Centralized logging setup
internal/proxy/        # Tunnel forwarding logic
```
- **internal/** prevents external imports (Go enforces at compile time)
- **cmd/** contains only thin mains; logic in internal/

### [H] TLS 1.3 for Control Plane
- Go 1.22 provides crypto/tls with TLS 1.3 by default
- **Requirement**: Server generates/loads certificate pair (self-signed OK for initial phase)
- **Pattern**: `tls.Listen("tcp", ":8443", &tls.Config{...})` for server
- **Validation**: Client must verify cert (or InsecureSkipVerify for dev only)

### [H] Bidirectional Protocol: Gob Encoding
- **encoding/gob**: Binary, type-safe, Go-native. No schema needed.
- **Performance**: ~10x faster than JSON for identical structs
- **Zero-copy**: Use `io.Copy()` for tunnel forwarding (kernel splice)
- **Alternative**: encoding/json for human debugging, but costs performance
- **Recommendation**: Gob for control plane + tunnel data, consider JSON for metrics

### [H] Structured Logging with slog (Go 1.21+)
- Built-in `log/slog` eliminates dependency on zerolog/logrus
- **Setup**: `slog.NewJSONHandler(os.Stdout, nil)` for production JSON logs
- **Per-component**: `logger := slog.With("component", "control_plane")`
- **Context integration**: Pass logger via context.Context for goroutines
- **Levels**: Debug, Info, Warn, Error (use sparingly in hot paths)

### [H] Networking Packages
**Required stdlib**:
- `net`: TCP listener, Dial, connection pooling
- `crypto/tls`: Secure control plane
- `context`: Goroutine cancellation, timeouts
- `io`: Zero-copy forwarding (io.Copy)
- `sync`: WaitGroup, Mutex (goroutine coordination)
- `encoding/gob`: Protocol marshaling

**Optional third-party**:
- `github.com/google/uuid`: Session/tunnel IDs (lightweight)
- Avoid bloat; vendoring increases binary size

### [M] Build & Test Configuration
**Makefile** recommended for dev convenience:
```make
build:
	go build -o ./bin/server ./cmd/server
	go build -o ./bin/client ./cmd/client

test:
	go test -v -race ./...

run-server:
	./bin/server -port 8443

run-client:
	./bin/client -server localhost:8443
```
- `-race` detects goroutine synchronization bugs (dev/CI only)
- `CGO_ENABLED=0` for static binaries (if no cgo needed)

### [M] Dependency Management
- **No external HTTP framework needed** for control plane (raw net.Listener sufficient)
- **No database required** for Phase 1 (in-memory session map)
- **Testing**: stdlib `testing` package + `net.Listener` for integration tests
- **Benchmarking**: `testing.B` for throughput validation

### [L] Go Version Rationale
- **Go 1.21+**: slog built-in, improved generics, better range
- **Go 1.22**: Range-over-int, improved http.ServeMux (not critical here)
- **Recommendation**: Target go 1.21 minimum for slog availability; 1.22 preferred

## References
- [golang-standards/project-layout](https://github.com/golang-standards/project-layout)
- [Go crypto/tls](https://pkg.go.dev/crypto/tls)
- [Go log/slog (1.21+)](https://pkg.go.dev/log/slog)
- [Go encoding/gob](https://pkg.go.dev/encoding/gob)
- [Go net package](https://pkg.go.dev/net)
