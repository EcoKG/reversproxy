# Phase 3 Report: HTTP/HTTPS 호스트 기반 라우팅

## Verdict: PASS

---

## Files Created/Modified

### Created
- `internal/tunnel/http_proxy.go` — HTTP proxy listener; reads Host header, looks up tunnel, relays via client data connection
- `internal/tunnel/https_proxy.go` — HTTPS SNI proxy; peeks TLS ClientHello, extracts SNI, relays raw TCP (no TLS termination at proxy)
- `internal/tunnel/ctrl_registry.go` — `ControlConnRegistry`: maps clientID → net.Conn for HTTP/HTTPS proxy to send OpenConnection messages
- `internal/tunnel/http_routing_test.go` — Integration tests for all 3 success criteria

### Modified
- `internal/protocol/messages.go` — Added `MsgRequestHTTPTunnel` (11), `MsgRequestHTTPSTunnel` (12), `MsgHTTPTunnelResp` (13), `RequestHTTPTunnel`, `RequestHTTPSTunnel`, `HTTPTunnelResp` structs
- `internal/tunnel/manager.go` — Added `HTTPTunnelEntry`, hostname→entry maps, `AddHTTPTunnel`, `AddHTTPSTunnel`, `GetHTTPTunnel`, `GetHTTPSTunnel`, `RemoveHTTPTunnelsForClient`
- `internal/control/handler.go` — Added `ctrlConns` variadic parameter; handle `MsgRequestHTTPTunnel` / `MsgRequestHTTPSTunnel`; cleanup on disconnect
- `internal/reconnect/config.go` — Added `HTTPTunnelConfig`, `HTTPSTunnelConfig`, `AddHTTPTunnel`, `AddHTTPSTunnel`
- `cmd/server/main.go` — Added `-http-addr`, `-https-addr` flags; start HTTP and HTTPS proxy listeners; pass `ctrlConns` to handler
- `cmd/client/main.go` — Added `-http-host`, `-http-port`, `-https-host`, `-https-port` flags; send `MsgRequestHTTPTunnel` / `MsgRequestHTTPSTunnel` on connect

---

## Tests

| Test | Description | Result |
|------|-------------|--------|
| TestSC1_HTTPHostRouting | Two clients with distinct hostnames; HTTP requests routed correctly | PASS |
| TestSC2_HTTPSRoutingBySNI | Two clients with distinct SNI hostnames; TLS echo verified end-to-end | PASS |
| TestSC3_UnknownHostReturnsError | HTTP 502 for unregistered Host header | PASS |
| TestSC3_UnknownSNIClosesConnection | Connection closed for unregistered SNI | PASS |

All existing Phase 1 & 2 regression tests: PASS (27 tests)

**Total: 31/31 PASS**

---

## Success Criteria

- **SC1: PASS** — 서로 다른 호스트명으로 들어오는 HTTP 요청이 각각 올바른 클라이언트의 서비스로 전달된다.
  `host-a.local` → "response-from-A", `host-b.local` → "response-from-B" — 완전히 분리되어 라우팅됨.

- **SC2: PASS** — HTTPS 요청도 호스트 기반으로 올바른 터널로 라우팅된다.
  TLS ClientHello에서 SNI를 파싱하여 `tls-a.local` / `tls-b.local` 각각 올바른 클라이언트로 라우팅.
  프록시는 TLS를 종료하지 않고 raw TCP를 relay하므로 클라이언트 측 TLS 서비스가 핸드쉐이크를 완료함.

- **SC3: PASS** — 존재하지 않는 호스트로 요청하면 명확한 에러 응답을 받는다.
  HTTP: `502 Bad Gateway — No tunnel registered for host "unknown.nonexistent"`.
  HTTPS: 연결이 즉시 닫힘 (TLS 핸드쉐이크 이전).

---

## Design Notes

- **HTTP relay strategy:** 단일 `:8080` 포트에서 HTTP 요청을 수신 → `Host` 헤더 파싱 → `ControlConnRegistry`를 통해 대상 클라이언트에 `OpenConnection` 전송 → 클라이언트의 data connection이 도착하면 HTTP 요청 bytes를 replay한 후 bidirectional relay.
- **HTTPS SNI strategy:** 단일 `:8445` 포트에서 원시 TCP 수신 → TLS ClientHello를 수동 파싱하여 SNI 추출 → 같은 OpenConnection 메커니즘으로 라우팅 → peeked bytes를 data connection에 replay한 후 relay. TLS 종료 없음.
- **ControlConnRegistry:** `tunnel.Manager`와 분리된 별도 레지스트리로 `clientID → net.Conn` 매핑. `HandleControlConn`이 variadic 파라미터로 수신하여 하위 호환성 유지.
- **Cleanup:** 클라이언트 연결 해제 시 `RemoveHTTPTunnelsForClient` + `ControlConnRegistry.Deregister` 모두 호출하여 메모리 누수 없음.
