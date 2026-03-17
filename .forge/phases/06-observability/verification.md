# Phase 6 Verification

## Build

```
go build ./...
```
Result: **PASS** — zero compilation errors.

## Test Run

```
go test -timeout 180s ./...
```

| Package | Result |
|---|---|
| internal/admin | ok (0.010s) |
| internal/config | ok (0.005s) |
| internal/control | ok (0.463s) |
| internal/protocol | ok (0.002s) |
| internal/reconnect | ok (0.314s) |
| internal/tunnel | ok (1.068s) |

All packages: **PASS**. No regressions.

## New Tests

### internal/admin
- `TestAdminAPI_Clients_Empty` — PASS
- `TestAdminAPI_Clients_AfterRegister` — PASS
- `TestAdminAPI_Tunnels_Empty` — PASS
- `TestAdminAPI_Tunnels_AfterRegister` — PASS
- `TestAdminAPI_Stats_Zeroed` — PASS
- `TestAdminAPI_Stats_Increments` — PASS
- `TestStats_AtomicCounters` — PASS
- `TestStats_TunnelRegistry` — PASS
- `TestStats_CountedReaderWriter` — PASS

### internal/config
- `TestLoadServerConfig_MissingFile` — PASS
- `TestLoadServerConfig_ValidFile` — PASS
- `TestLoadServerConfig_InvalidYAML` — PASS
- `TestLoadClientConfig_MissingFile` — PASS
- `TestLoadClientConfig_ValidFile` — PASS
- `TestDefaultServerConfig` — PASS
- `TestDefaultClientConfig` — PASS

## Success Criteria Verification

### SC1: 현재 연결된 클라이언트 목록과 활성 터널 정보를 조회할 수 있다
- `GET /api/clients` returns all clients registered in `ClientRegistry` with id, name, addr, connected_at.
- `GET /api/tunnels` returns all TCP/HTTP/HTTPS tunnels from `Manager.ListTunnels()` + `Manager.ListHTTPTunnels()`.
- Verified by `TestAdminAPI_Clients_AfterRegister` and `TestAdminAPI_Tunnels_AfterRegister`.
- **PASS**

### SC2: 터널링 트래픽 통계(연결 수, 전송량)를 확인할 수 있다
- `GET /api/stats` returns total_connections, active_connections, bytes_in, bytes_out from `stats.ServerStats` (atomic.Int64).
- Per-tunnel stats tracked in `stats.Registry` (connection_count, bytes_in, bytes_out) and included in stats response.
- `CountedReader`/`CountedWriter` wrappers track bytes through tunnels.
- Verified by `TestAdminAPI_Stats_Increments`, `TestStats_AtomicCounters`, `TestStats_TunnelRegistry`, `TestStats_CountedReaderWriter`.
- **PASS**

### SC3: 비정상 상황(연결 실패, 터널 오류) 발생 시 로그에서 원인을 파악할 수 있다
- All error paths in `control/handler.go`, `tunnel/server.go`, `tunnel/http_proxy.go`, `tunnel/https_proxy.go` emit structured slog entries with the error value.
- Logger enhanced with `NewWithLevel(component, level)` to filter by severity (debug/info/warn/error).
- Log level configurable via config file or `--log-level` flag.
- `cmd/client/main.go` adds `"known_tunnels"` field to OpenConnection-for-unknown-tunnel warning.
- **PASS**

### SC4: 설정 파일을 통해 서버와 클라이언트의 동작을 구성할 수 있다
- `internal/config/config.go` implements YAML-based config loading via `gopkg.in/yaml.v3`.
- `LoadServerConfig(path)` and `LoadClientConfig(path)` merge YAML values onto defaults; missing file = no error.
- Both `cmd/server/main.go` and `cmd/client/main.go` accept `--config` flag and apply flag overrides via `flag.Visit`.
- Default config file path is `config.yaml` in the working directory.
- Verified by all `TestLoadServerConfig_*` and `TestLoadClientConfig_*` tests.
- **PASS**
