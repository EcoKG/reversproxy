# Research Report: Phase 1 — Control Plane 연결

## Summary

**Goal:** Client ↔ Proxy Server 간 양방향 제어 연결 수립 (등록, 식별, 단절 감지)

**핵심 결정:**
1. **프로토콜:** TLS 1.3 위 Length-prefixed binary (4-byte big-endian + gob 인코딩)
2. **인증:** Pre-shared token in ClientRegister 메시지 (mTLS는 Phase 2)
3. **연결 모델:** Control plane = 클라이언트당 TCP 1개. 멀티플렉싱은 Phase 2
4. **Heartbeat:** TCP keepalive(15s) + App-level Ping/Pong(30s 간격, 2회 미응답 시 종료)
5. **클라이언트 식별:** Server-assigned UUID; `map[clientID]*Client` registry with RWMutex
6. **단절 감지:** Explicit Disconnect 메시지 + heartbeat 타임아웃 (양방향 모두)
7. **로깅:** `log/slog` JSON handler, component-tagged loggers
8. **빌드:** Go 1.22, cmd/server + cmd/client, internal/ 격리, Makefile

**권장 메시지 타입 (Phase 1 최소 집합):**
`ClientRegister`, `RegisterResp`, `Ping`, `Pong`, `Disconnect`, `DisconnectAck`

---

## HIGH Priority Findings

### [H1] Length-Prefixed Binary Framing — Industry Standard

TCP는 스트림 기반이므로 메시지 경계가 없다. ngrok은 64-bit LE length prefix, frp는 binary serialization을 사용한다. 경계 없이 구현하면 TCP segment 분할 시 파싱 실패.

**결정:** `[4-byte big-endian uint32 length][payload bytes]`
- Payload 인코딩: `encoding/gob` (Go-native, 타입 안전, JSON 대비 ~10x 빠름)
- 디버깅 시 JSON fallback 고려 가능하나 Phase 1에서는 gob 사용
- 최대 메시지 크기 제한 권장 (DoS 방지, 예: 1MB)

**액션:** `internal/protocol/` 에 `ReadMessage(conn)` / `WriteMessage(conn, msg)` 구현

### [H2] 동기식 Registration Handshake 필수

클라이언트가 연결된 즉시 터널 요청을 보내면 안 된다. frp/ngrok 모두 등록 완료 후에만 다음 단계 진행.

**플로우:**
```
Client → Server: ClientRegister{client_id(empty), auth_token, name, version}
Server → Client: RegisterResp{assigned_client_id, server_id, status}
         [등록 실패 시 즉시 연결 종료]
Client: ready state 진입
```

**서버 측:** `sync.RWMutex` 보호된 `map[string]*Client` registry. 등록 시 slog로 기록:
`"client registered" id=abc123 name=my-client addr=10.0.0.1:54321`

**액션:** `internal/control/registry.go` — ClientRegistry struct 구현

### [H3] Heartbeat + TCP Keepalive 조합 필수

Linux 기본 설정에서 dead TCP 감지에 ~11분 소요. 이를 허용하면 좀비 클라이언트가 registry를 오염시킨다.

**두 레이어 모두 필요한 이유:**
- TCP keepalive: NAT/방화벽 통과, OS 커널이 관리 (네트워크 파티션 감지)
- App heartbeat: 프록시 크래시/hang 감지, 로직 레벨 제어 가능

**설정:**
```go
tcpConn.SetKeepAlive(true)          // Go 1.13+ 기본 15s idle
tcpConn.SetKeepAlivePeriod(15 * time.Second)

// App-level: server → client Ping every 30s
// Client responds with Pong within 10s
// 2 consecutive misses → close + log "heartbeat timeout"
```

**액션:** `internal/control/heartbeat.go` — ticker 기반 Ping 발송 + deadline 리셋

### [H4] Control / Data Plane 분리

Phase 1은 control plane만 구현하지만, 설계 단계부터 분리가 필요하다. control 실패 시 기존 데이터 터널이 유지되어야 하고, data plane 에러가 control 메시지를 block해서는 안 된다.

**디렉토리 반영:**
```
internal/control/   ← Phase 1: 이곳만 구현
internal/proxy/     ← Phase 2: data plane (지금은 stub 또는 빈 패키지)
```

**액션:** Phase 1 범위를 `internal/control/` 및 `internal/protocol/` 로 한정

### [H5] TLS 1.3 — 제어 채널 암호화 필수

평문 TCP로 제어 연결을 열면 auth_token이 노출된다. Phase 1부터 TLS 사용 필요.

**구현:**
- Server: `tls.Listen("tcp", ":8443", tlsConfig)` (self-signed cert 허용)
- Client: `tls.Dial("tcp", addr, tlsConfig)` — 개발 중 `InsecureSkipVerify: true` 허용, 프로덕션 시 제거
- 인증서 생성: `go generate` 또는 Makefile에 `openssl` 커맨드 포함

**액션:** `internal/control/tls.go` — cert load/generate 헬퍼

---

## MEDIUM Priority Findings

### [M1] 프로젝트 구조 — 표준 Go Layout

```
cmd/server/main.go          # thin main, flags 파싱
cmd/client/main.go          # thin main, flags 파싱
internal/control/           # registry, heartbeat, handler, tls
internal/protocol/          # 메시지 타입 정의 + framing I/O
internal/logger/            # slog 초기화, component helpers
internal/proxy/             # Phase 2 (stub only)
Makefile                    # build, test, run-server, run-client
go.mod                      # github.com/starlyn/reversproxy
```

`internal/`은 Go 컴파일러가 외부 import를 금지 → 캡슐화 보장.

### [M2] 클라이언트 고유 식별 — Server-Assigned UUID

동일 호스트에서 여러 클라이언트가 실행될 수 있으므로 hostname만으로는 부족하다.

**권장 구조:**
```go
type Client struct {
    ID               string       // server-assigned UUID
    Name             string       // client-provided label
    Addr             string       // remote address
    RegisteredAt     time.Time
    LastHeartbeatAt  time.Time
    Conn             net.Conn
    cancelFn         context.CancelFunc
}
```

Server는 `RegisterResp`에 UUID 포함하여 반환. Client는 이후 모든 메시지에 이 ID 사용.

### [M3] Graceful Shutdown — Explicit Disconnect

단순 소켓 종료만으로는 상대방이 즉시 감지 못 할 수 있다.

**플로우:**
```
Client/Server → Disconnect{reason}
상대방 → DisconnectAck (선택) → 소켓 close
타임아웃: 2초 내 응답 없으면 force-close
```

SIGTERM 핸들러에서 모든 클라이언트에 Disconnect 브로드캐스트 후 graceful shutdown.

### [M4] Goroutine-per-Connection 모델

```go
// Server accept loop
for {
    conn, err := listener.Accept()
    if err != nil { break }
    go handleControlConn(ctx, conn, registry)
}

// Per-connection handler
func handleControlConn(ctx context.Context, conn net.Conn, reg *ClientRegistry) {
    defer conn.Close()
    // registration → heartbeat → message loop
}
```

Goroutine 비용: ~2KB/개. 10K 클라이언트 = ~20MB (허용 범위). `-race` 플래그로 개발 중 race condition 검출.

### [M5] 의존성 최소화

Phase 1에서 외부 의존성:
- `github.com/google/uuid` — UUID 생성 (경량, 검증된 라이브러리)
- 그 외 stdlib만 사용 (`net`, `crypto/tls`, `encoding/gob`, `log/slog`, `context`, `sync`)

멀티플렉싱 라이브러리(yamux, smux)는 Phase 2까지 추가 금지.

---

## LOW Priority Findings

### [L1] WebSocket Fallback (Phase 2+ 고려)

표준 TCP가 aggressive NAT/기업 방화벽 통과에 실패할 수 있다. WebSocket over TLS(wss://)는 HTTP 프록시 환경에서도 통과 가능. Phase 1에서는 구현 불필요, 아키텍처 설계 시 transport layer 추상화 유지.

### [L2] Go 1.22 vs 1.21

`log/slog`는 1.21부터 stdlib 포함. Range-over-int 등 1.22 기능은 이 프로젝트에서 크게 중요하지 않음. go.mod에 `go 1.22` 명시 권장 (최신 안정 버전 사용).

### [L3] Keepalive vs Heartbeat — 역할 명확화

두 메커니즘의 역할 혼동 주의:
- TCP keepalive = 네트워크 레벨 연결 생존 확인 (OS 관리)
- App heartbeat = 애플리케이션 로직 레벨 생존 확인 (우리 코드 관리)
두 가지를 모두 구현하되 설정값은 분리하여 튜닝 가능하게.

---

## Conflicts Resolved

| 주제 | research-arch | research-stack | research-patterns | 결정 |
|------|--------------|----------------|-------------------|------|
| 인코딩 | binary/protobuf 또는 msgpack | gob 권장 | protobuf 권장 | **gob** (외부 의존성 없음, Phase 1 충분) |
| 멀티플렉싱 | Phase 2로 defer | 언급 없음 | Phase 2로 defer | **Phase 2 defer** (3개 모두 동의) |
| TLS | Phase 1부터 | Phase 1부터 | Phase 1부터 | **Phase 1 필수** (전원 동의) |
| Auth | token 기반 | TLS + gob | token 기반 | **pre-shared token** (mTLS는 Phase 2) |

---

## Recommendations for Planner

- Phase 1 구현 범위를 `internal/control/` + `internal/protocol/` 두 패키지로 한정한다
- 메시지 타입 정의(`internal/protocol/messages.go`)를 가장 먼저 구현한다 — 모든 로직이 이에 의존
- TLS 설정을 초기부터 포함한다 (나중에 추가하면 리팩토링 비용이 크다)
- 서버 accept loop → registration handler → heartbeat ticker 순서로 구현한다
- 각 성공 기준을 테스트로 검증한다:
  - SC1: 클라이언트 연결 후 서버 로그에 `"client registered"` 출력
  - SC2: 다수 클라이언트 동시 연결 시 각 UUID 독립 확인 (integration test)
  - SC3: 클라이언트 종료 후 서버에서 heartbeat timeout 또는 EOF 감지 로그 확인
- `go test -race ./...` 를 CI에서 필수 실행 (goroutine 경쟁 조건 조기 발견)
- Phase 1 완료 후 Phase 2(data plane + multiplexing)로 전환 전 `internal/proxy/` stub 파일만 준비

---

## References

### Architecture & Patterns
- [FRP GitHub](https://github.com/fatedier/frp)
- [ngrok Protocol Design](https://github.com/inconshreveable/ngrok/blob/master/docs/DEVELOPMENT.md)
- [Reverse Proxy Tunnel - GOST](https://gost.run/en/tutorials/reverse-proxy-tunnel/)
- [Awesome Tunneling Projects](https://github.com/anderspitman/awesome-tunneling)
- [Why Decoupling Control and Data Planes](https://thenewstack.io/why-decoupling-control-and-data-planes-is-the-future-of-saas/)

### Go Stack
- [golang-standards/project-layout](https://github.com/golang-standards/project-layout)
- [Go net package](https://pkg.go.dev/net)
- [Go crypto/tls](https://pkg.go.dev/crypto/tls)
- [Go encoding/gob](https://pkg.go.dev/encoding/gob)
- [Go log/slog](https://pkg.go.dev/log/slog)
- [Go context patterns](https://go.dev/blog/context)
- [High-Performance Network Services in Go](https://oneuptime.com/blog/post/2026-02-01-go-high-performance-network-services/view)

### Protocol References
- [Length-Prefix Framing](https://eli.thegreenplace.net/2011/08/02/length-prefix-framing-for-protocol-buffers)
- [RabbitMQ Heartbeats](https://www.rabbitmq.com/docs/heartbeats)
- [Using TCP Keepalive with Go](https://felixge.de/2014/08/26/using-tcp-keepalive-with-go/)
- [Yamux (HashiCorp)](https://github.com/hashicorp/yamux) — Phase 2 참고
- [Smux (xtaci)](https://github.com/xtaci/smux) — Phase 2 참고
