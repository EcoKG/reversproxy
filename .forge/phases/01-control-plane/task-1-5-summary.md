# Task 1-5 Summary — TLS Config Helpers

## Status: DONE

## File Created
- `internal/control/tls.go` (91 lines)

## Exported Symbols
| Symbol | Signature | Purpose |
|---|---|---|
| `LoadOrGenerateCert` | `(certFile, keyFile string) (tls.Certificate, error)` | Loads existing PEM pair or generates self-signed ECDSA P-256 cert and writes PEM files |
| `NewServerTLSConfig` | `(cert tls.Certificate) *tls.Config` | Returns TLS 1.3 server config with given certificate |
| `NewClientTLSConfig` | `(insecureSkipVerify bool) *tls.Config` | Returns TLS 1.3 client config; `insecureSkipVerify=true` for dev against self-signed certs |

## Key Implementation Details
- Self-signed cert: ECDSA P-256, valid 10 years, SAN includes `127.0.0.1` and `localhost`, `IsCA: true`
- PEM files written with permissions `0644` (cert) and `0600` (key)
- `MinVersion: tls.VersionTLS13` enforced in both server and client configs
- Internal helper `fileExists` used to check both cert and key before attempting load

## Acceptance Criteria Verification
| Criterion | Result |
|---|---|
| `grep "func LoadOrGenerateCert"` | PASS |
| `grep "func NewServerTLSConfig"` | PASS |
| `grep "func NewClientTLSConfig"` | PASS |
| `grep "tls.VersionTLS13"` | PASS (appears twice: server + client) |
| `go build ./internal/control/...` | PASS |
| `go build ./...` | PASS |

## Dependencies Satisfied
- Wave 1, task 1-1: `go.mod` with module `github.com/starlyn/reversproxy` — present
- Wave 1, task 1-3: `internal/logger/logger.go` — present (not imported by tls.go; no dependency needed)
