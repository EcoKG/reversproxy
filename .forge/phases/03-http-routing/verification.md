# Phase 3 Verification: HTTP/HTTPS 호스트 기반 라우팅

## Build

```
go build ./...
exit: 0
```

## Vet

```
go vet ./...
exit: 0
```

## Test Run

```
go test ./... -v -timeout 60s
```

### Results

| Package | Result | Duration |
|---------|--------|----------|
| github.com/starlyn/reversproxy/internal/control | ok | 0.462s |
| github.com/starlyn/reversproxy/internal/protocol | ok | 0.002s |
| github.com/starlyn/reversproxy/internal/reconnect | ok | 0.313s |
| github.com/starlyn/reversproxy/internal/tunnel | ok | 0.344s |

**Total: 31 PASS, 0 FAIL**

### Phase 3 Specific Tests

| Test | Result |
|------|--------|
| TestSC1_HTTPHostRouting | PASS (0.06s) |
| TestSC2_HTTPSRoutingBySNI | PASS (0.06s) |
| TestSC3_UnknownHostReturnsError | PASS (0.02s) |
| TestSC3_UnknownSNIClosesConnection | PASS (0.02s) |

### Regression Tests (Phase 1 & 2)

| Test | Result |
|------|--------|
| TestSC1_ClientRegistration | PASS |
| TestSC2_MultipleClients | PASS |
| TestSC3_DisconnectionDetected | PASS |
| TestSC1_ExternalConnectionReachesLocalService | PASS |
| TestSC2_DataIntegrity | PASS |
| TestSC3_ConcurrentConnections | PASS |
| TestSC1_AutoReconnectAfterServerRestart | PASS |
| TestSC2_TunnelRestoredAfterReconnect | PASS |
| TestSC3_ExponentialBackoff | PASS |

## Key Observations

- **SC1 (HTTP routing):** `host-a.local` → "response-from-A", `host-b.local` → "response-from-B".
  Two clients with different hostnames both received their correct responses with no cross-routing.

- **SC2 (HTTPS routing):** `tls-a.local` SNI → TLS echo A, `tls-b.local` SNI → TLS echo B.
  The SNI parser correctly extracted both hostnames from TLS ClientHello records.
  TLS was NOT terminated at the proxy; the local TLS server completed the handshake end-to-end.

- **SC3 (Unknown host):** HTTP 502 response with "No tunnel registered for host" message.
  HTTPS connection with unknown SNI was closed immediately (before TLS handshake).

## Date

2026-03-17
