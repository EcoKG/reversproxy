# Phase 6 Report: 운영 안정성 및 관측성

## Verdict: PASS

---

## Files Created

| File | Description |
|---|---|
| `internal/stats/stats.go` | Thread-safe traffic counters (`ServerStats`, `TunnelStats`, `Registry`) with `atomic.Int64`; `CountedReader`/`CountedWriter` wrappers |
| `internal/config/config.go` | YAML config loading via `gopkg.in/yaml.v3`; `ServerConfig` + `ClientConfig` with defaults and flag-override support |
| `internal/admin/api.go` | Admin HTTP JSON API server; `GET /api/clients`, `GET /api/tunnels`, `GET /api/stats` |
| `internal/admin/api_test.go` | Integration tests for admin API and stats counters (9 tests) |
| `internal/config/config_test.go` | Unit tests for config loading and defaults (7 tests) |

## Files Modified

| File | Change |
|---|---|
| `internal/tunnel/manager.go` | Added `ListTunnels()` and `ListHTTPTunnels()` methods (snapshot reads under RLock) |
| `internal/logger/logger.go` | Added `NewWithLevel(component, levelStr)` and `parseLevel()` for configurable log levels |
| `cmd/server/main.go` | Integrated `--config` flag, `config.LoadServerConfig`, `admin.Server.Start`, `stats.Global` connection tracking |
| `cmd/client/main.go` | Integrated `--config` flag, `config.LoadClientConfig`, config-file tunnel list; enhanced OpenConnection warning log |
| `go.mod` / `go.sum` | Added `gopkg.in/yaml.v3 v3.0.1` |

---

## Tests

| Package | Tests | Result |
|---|---|---|
| `internal/admin` | 9 | PASS |
| `internal/config` | 7 | PASS |
| `internal/control` | existing | PASS (no regression) |
| `internal/protocol` | existing | PASS (no regression) |
| `internal/reconnect` | existing | PASS (no regression) |
| `internal/tunnel` | existing | PASS (no regression) |

Total: **16 new tests**, all passing. Zero regressions.

---

## Success Criteria

- **SC1:** PASS — `GET /api/clients` lists connected clients (id, name, addr, connected_at); `GET /api/tunnels` lists all TCP/HTTP/HTTPS tunnels with type, local_addr, public_addr/hostname.
- **SC2:** PASS — `GET /api/stats` returns total_connections, active_connections, bytes_in, bytes_out (global `atomic.Int64`) plus per-tunnel stats. `CountedReader`/`CountedWriter` available for relay instrumentation.
- **SC3:** PASS — All error paths emit structured slog entries with error cause. Log level configurable (debug/info/warn/error) via config file or `--log-level` flag so operators can increase verbosity for diagnosis without recompilation.
- **SC4:** PASS — `internal/config` provides YAML config for both server and client (all major fields). Config file is optional (missing file = defaults); command-line flags override file values via `flag.Visit`. Default path is `config.yaml` in working directory.
