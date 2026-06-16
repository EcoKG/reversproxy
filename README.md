# reversproxy

> 역방향(reverse-dial) 터널 프록시 — **클라이언트가 리슨(listen)하고, 서버가 다이얼(dial)합니다.** NAT/방화벽 뒤에 있는 클라이언트에 공인 서버가 직접 접속하여 터널을 수립하는, 방향이 뒤집힌 리버스 터널입니다.

`github.com/EcoKG/reversproxy` 는 Go로 작성된 역방향 터널 프록시입니다. 일반적인 ngrok 류 도구와 달리, **클라이언트가 TLS 리스너를 열고 대기하며 공인 서버가 각 클라이언트로 다이얼아웃**합니다. 따라서 클라이언트는 제어 평면에서 외부로의 아웃바운드 연결을 직접 열지 않으면서도, 공인 서버가 NAT 뒤의 내부 서비스를 인터넷에 노출하거나, 반대로 클라이언트에서 발생한 트래픽을 서버를 출구 노드(exit node)로 삼아 인터넷으로 내보낼 수 있습니다.

## 핵심 특징

- **역방향 연결 모델**: 제어 평면(control plane)은 클라이언트가 `tls.Listen`(기본 `:8443`)으로 대기하고, 서버가 설정된 각 클라이언트로 `tls.Dial`합니다. 일반적인 프록시와 연결 방향이 정반대입니다.
- **두 방향의 데이터 흐름**:
  - **노출(expose)**: 서버가 공개 TCP/HTTP/HTTPS 리스너를 열고, 외부 요청을 클라이언트의 로컬 서비스로 터널링합니다.
  - **출구(exit)**: 클라이언트가 로컬 SOCKS5 / HTTP CONNECT / 포트 포워드 리스너를 열고, 서버를 인터넷 출구 노드로 사용합니다.
- **멀티 서버 / 멀티 클라이언트**: 서버는 여러 클라이언트로 동시에 다이얼하고, 클라이언트는 여러 서버의 접속을 동시에 수용(`ServerPool`)하며 클라이언트발 트래픽을 라운드로빈으로 분배합니다.
- **TOFU 인증서 핀닝 + 운영자 승인**: 서버는 각 클라이언트의 TLS leaf 인증서 SHA-256 지문을 `known_hosts.yaml`에 기록하고, 미등록 클라이언트는 운영자 승인(또는 `auto_approve`) 전까지 차단합니다. 지문 불일치는 MITM 가능성으로 간주하여 거부합니다.
- **내장 웹 대시보드 + REST API + SSE**: 서버 바이너리에 임베드된 단일 HTTP 서버가 대시보드와 JSON API, 실시간 이벤트 스트림(Server-Sent Events)을 제공합니다.
- **자동 재연결**: 다이얼하는 쪽(서버)이 지수 백오프(1초 → 60초, ±20% 지터)로 재연결합니다.
- **TLS 1.3 강제**: 양쪽 모두 최소 버전 TLS 1.3. 자체 서명 인증서를 자동 생성합니다.
- **관측성**: 락-프리 원자 카운터 기반 통계, `component` 태그가 붙은 JSON 구조화 로깅(slog).

> **보안 주의**: 기본 설정은 개발 편의를 위한 값입니다. `auth_token` 기본값은 `changeme`, `insecure` 기본값은 `true`(TLS 검증 생략), `admin_token` 기본값은 빈 문자열(관리 API 무인증)입니다. 운영 환경에서는 반드시 토큰을 교체하고 `insecure: false`로 설정하시기 바랍니다.

---

## 아키텍처 다이어그램

```
                          제어 평면(Control Plane) — 방향이 뒤집힘
                          서버가 클라이언트로 다이얼아웃

  ┌───────────────────────────┐                 ┌──────────────────────────────┐
  │   Proxy SERVER (공인)       │                 │   CLIENT (NAT/방화벽 뒤)        │
  │                           │                 │                              │
  │  dialClientLoop ─tls.Dial───────────────────►  tls.Listen :8443            │
  │   (지수 백오프 재연결)        │   TLS 1.3 제어    │   (서버 인증서를 제시)            │
  │                           │   연결(영속)       │                              │
  │  TOFU 지문 핀닝            ◄── MsgRegisterResp ─┤  토큰 검증 후 등록 응답          │
  │  known_hosts.yaml         │                 │                              │
  │                           │                 │                              │
  │  공개 리스너:               │                 │  로컬 서비스:                   │
  │   data_addr   :8444 ◄──── 데이터 conn ────────┤  (외부 연결마다 클라이언트가     │
  │   http_addr   :8080       │  (클라이언트가 서버로  │   서버 :8444로 다이얼백)         │
  │   https_addr  :8445       │   다이얼백)          │   127.0.0.1:<local_port>     │
  │   admin_addr  :9090       │ (127.0.0.1 전용)    │                              │
  │   <requested_port>        │ (터널별 공개 포트)    │  클라이언트 측 프런트엔드:        │
  │                           │                 │   SOCKS5       :1080          │
  │  인터넷 출구 노드 ◄── MsgSOCKSData(제어 conn 멀티 │   HTTP CONNECT :8080         │
  │  (DNS 서버에서 해석)          플렉싱) ───────────┤   port_forwards <local>      │
  └───────────────────────────┘                 └──────────────────────────────┘
        │                                                       ▲
        ▼                                                       │
   인터넷 / 외부 클라이언트                                 SOCKS/CONNECT/RDP 등
   (http/https/tcp 요청)                                  로컬 애플리케이션
```

- **제어 평면**: 서버 → 클라이언트로 다이얼. 클라이언트가 TLS 서버 역할(인증서 제시), 서버가 TLS 클라이언트 역할(TOFU 지문 핀닝).
- **노출용 데이터 평면**(터널/HTTP/HTTPS): 외부 연결 1건마다 클라이언트가 서버의 데이터 포트(`:8444`)로 새 데이터 연결을 **다이얼백**합니다(멀티플렉싱 없음).
- **출구용 데이터 평면**(SOCKS5/CONNECT/port-forward): 모든 스트림이 단일 제어 연결 위에서 `MsgSOCKSData` 프레임으로 **멀티플렉싱**됩니다(별도 데이터 포트 불필요).

---

## 동작 원리 / 워크플로우

### 1. 연결 수립 (방향 역전)

1. 클라이언트가 `listen_addr`(기본 `:8443`)에서 `tls.Listen`으로 대기합니다. 클라이언트가 TLS **서버** 인증서를 제시합니다.
2. 서버가 `clients[]`의 각 항목으로 `tls.Dial`(지수 백오프)을 수행합니다. 서버가 TLS **클라이언트(다이얼러)** 입니다.
3. TLS 1.3 핸드셰이크가 먼저 완료됩니다.

### 2. TOFU 승인 흐름 (서버 측)

1. 핸드셰이크 직후, 프로토콜 메시지 교환 **이전에** 서버가 클라이언트의 leaf 인증서 지문 `sha256:<hex>`를 계산합니다.
2. 판정(클라이언트 **이름** 기준):
   - **등록 + 일치** → 즉시 수락.
   - **등록 + 불일치** → 거부. MITM 가능성으로 간주, `client.fingerprint_mismatch` 이벤트 발행 및 보안 로그 기록 후 재다이얼.
   - **미등록 + `auto_approve: true`** → 신뢰하고 `known_hosts.yaml`에 영구 저장(개발용).
   - **미등록 + `auto_approve: false`(기본)** → `client.pending` 이벤트 발행 후 **운영자 승인까지 차단**.
3. 운영자는 관리 API `POST /api/decide?name=<client>&action=approve|reject`로 결정합니다. 승인 시 지문이 `known_hosts.yaml`에 저장되고 핸드셰이크가 진행되며, 거부 시 재다이얼합니다.
4. 어느 경로로든 최종 수락되면(등록+일치 / `auto_approve` / 운영자 승인) `dialClientLoop`가 공통으로 `client.connected` 이벤트를 발행합니다. 운영자 승인 분기에서는 `client.approved`, 거부 분기에서는 `client.rejected`가 추가로 발행됩니다.

### 3. 등록 핸드셰이크 (방향 역전)

1. **서버가 먼저** `MsgClientRegister{AuthToken, Name:"server", Version:"0.1.0"}`를 전송합니다(핸드셰이크 타임아웃 10초).
2. **클라이언트가** 토큰을 검증합니다(불일치 시 `MsgRegisterResp{Status:"error"}` 후 종료). 성공 시 `MsgRegisterResp{Status:"ok", ServerID:<클라이언트 이름>}`로 응답합니다.
3. 서버가 응답을 받아 클라이언트를 레지스트리에 등록(UUID 부여)하고 하트비트를 시작합니다.

### 4. 하트비트 / 재연결

1. 서버가 `HeartbeatInterval`(10초)마다 `MsgPing`을 보내고, 클라이언트가 `MsgPong`으로 응답합니다.
2. `time.Since(LastHeartbeat) > 30초`이고 누락 횟수 ≥ 2이면 타임아웃으로 간주하여 연결을 해제합니다.
3. 다이얼하는 쪽인 **서버**가 지수 백오프(초기 1초, ×2, 상한 60초, ±20% 지터)로 재연결합니다. 클라이언트는 재연결하지 않고 새 접속을 다시 수용(Accept)합니다.

### 5. 터널 등록 및 데이터 흐름 (노출 경로)

1. 클라이언트가 자신의 `tunnels[]` 항목마다 `MsgRequestTunnel`/`MsgRequestHTTPTunnel`/`MsgRequestHTTPSTunnel`을 보냅니다.
2. 서버가 공개 포트를 열거나(raw TCP) 호스트명을 라우팅 맵에 등록(HTTP/HTTPS)하고, 할당된 공개 포트와 데이터 주소로 응답합니다.
3. 외부 요청이 들어오면 서버가 `MsgOpenConnection`을 클라이언트로 보냅니다.
4. **클라이언트가 서버의 데이터 포트(`:8444`)로 다이얼백**하고 첫 프레임으로 `MsgDataConnHello{ConnID}`를 보낸 뒤 로컬 서비스로 연결합니다.
5. 서버가 대기 중이던 외부 연결과 매칭하여 양방향 릴레이를 시작합니다.
   - **HTTP**: `Host` 헤더만 파싱하여 라우팅 후 원본 요청 바이트를 그대로 재전송(TLS 종료 안 함).
   - **HTTPS**: TLS ClientHello의 SNI만 들여다보고(peek) 라우팅 후 암호화 스트림을 그대로 전달(**TLS는 클라이언트의 로컬 서비스에서만 종료**).

### 6. 출구 경로 (SOCKS5 / HTTP CONNECT / port-forward)

1. 클라이언트가 로컬 리스너(SOCKS5 `:1080`, HTTP CONNECT `:8080`, port-forward `<local_port>`)를 엽니다.
2. 로컬 애플리케이션이 접속하면 클라이언트가 `MsgSOCKSConnect{ConnID, TargetHost, TargetPort}`를 **기존 제어 연결**로 전송합니다.
3. **서버가 인터넷 대상을 직접 다이얼**(DNS도 서버 측에서 해석)하고 `MsgSOCKSReady`로 응답합니다.
4. 모든 페이로드가 `MsgSOCKSData`/`MsgSOCKSClose` 프레임으로 단일 제어 연결 위에서 멀티플렉싱됩니다(별도 데이터 포트 불필요).

---

## 주요 기능 표

| 기능 | 설명 | 위치 |
|------|------|------|
| 역방향 제어 연결 | 클라이언트 리슨, 서버 다이얼 | 클라이언트 `:8443` / 서버 다이얼 |
| TCP 터널 | 외부 공개 포트 → 클라이언트 로컬 서비스 | 서버 (공개 포트 + 데이터 `:8444`) |
| HTTP 호스트 라우팅 | `Host` 헤더 기반 라우팅, 원본 요청 재전송 | 서버 `:8080` |
| HTTPS SNI 라우팅 | TLS SNI 기반 라우팅, TLS 미종료 | 서버 `:8445` |
| SOCKS5 프록시 | CONNECT 전용, RFC 1929 인증 지원 | 클라이언트 `:1080` |
| HTTP CONNECT 프록시 | 인증 없음, SOCKS mux 재사용 | 클라이언트 `:8080` |
| 정적 포트 포워드 | 로컬 포트 → 서버 경유 → 원격 대상 | 클라이언트 `<local_port>` |
| TOFU 인증서 핀닝 | 클라이언트 지문 핀닝 + 운영자 승인 | 서버 `known_hosts.yaml` |
| 웹 대시보드 + REST + SSE | 임베드 UI, 8개 API, 실시간 이벤트 | 서버 `127.0.0.1:9090` |
| 멀티 서버 수용 | 클라이언트가 다수 서버 동시 수용, 라운드로빈 | 클라이언트 `ServerPool` |
| 멀티 클라이언트 다이얼 | 서버가 다수 클라이언트로 다이얼 | 서버 `clients[]` |
| 레이트 리미팅 | per-IP 토큰 버킷 + 전역 동시성 제한 (HTTP/HTTPS 프록시 한정) | 서버 |
| 자동 재연결 | 지수 백오프 1s→60s, ±20% 지터 | 서버 |

---

## 설치

릴리스 워크플로(GitHub Actions)는 태그 푸시(`v*`) 시 6개의 빌드 잡을 정의하지만, **현재 `cmd/winclient`(Windows systray GUI)가 컴파일되지 않아** 릴리스 잡이 실패하며, 실제로 산출되는 아티팩트는 다음 5종입니다(아래 "프로젝트 구조" 참고).

| 아티팩트 | 대상 | 비고 |
|----------|------|------|
| `reversproxy-client-linux-amd64` | Linux 클라이언트 (x86_64) | CLI |
| `reversproxy-client-linux-arm64` | Linux 클라이언트 (arm64) | CLI |
| `reversproxy-server-linux-amd64` | Linux 서버 (x86_64) | |
| `reversproxy-server-linux-arm64` | Linux 서버 (arm64) | |
| `reversproxy-server-windows-amd64.exe` | Windows 서버 | CLI |

> ⚠ `reversproxy-client-windows-amd64.exe`(systray GUI, `cmd/winclient`)는 멀티 서버 리팩토링 이후 빌드가 깨져 있어 현재 배포되지 않습니다. Windows에서 클라이언트가 필요하면 CLI 클라이언트(`cmd/client`)를 직접 빌드해 사용하시기 바랍니다.

### Linux 클라이언트 (원라이너)

```bash
curl -fsSL https://raw.githubusercontent.com/EcoKG/reversproxy/master/scripts/install-client.sh | bash
```

이 스크립트는 다음을 수행합니다:

1. 아키텍처(amd64/arm64)를 감지하여 최신 릴리스에서 `reversproxy-client`를 `~/reversproxy/`에 내려받습니다.
2. `rproxy` 헬퍼 스크립트를 생성합니다(하드코딩 값: `LISTEN=:8443`, `TOKEN=changeme`, `NAME=$(hostname)`, `LOCAL_PORT=80`, `SOCKS=:1080`, `HTTP_PROXY=:8080`). `rproxy {start|stop|status|logs|restart}`로 사용합니다.
3. `~/.bashrc`와 `~/.zshrc`에 PATH 추가와 함께 **프록시 환경변수를 주입**합니다: `HTTPS_PROXY`/`HTTP_PROXY=http://127.0.0.1:8080`, `ALL_PROXY=socks5h://127.0.0.1:1080`, `NO_PROXY=localhost,127.0.0.1`.

> 환경변수 주입을 원하지 않으시면 이 스크립트 대신 바이너리만 내려받아 직접 실행하시기 바랍니다.

### Windows 서버 (PowerShell, 관리자 권한)

```powershell
irm https://raw.githubusercontent.com/EcoKG/reversproxy/master/scripts/install-server.ps1 | iex
```

이 스크립트는 다음을 수행합니다:

1. 최신 릴리스에서 `reversproxy-server.exe`를 `%ProgramFiles%\reversproxy`에 내려받습니다.
2. 기본 `server.yaml`을 생성합니다(`auth_token: changeme`, `clients` 예시 포함 — **`known_hosts_path`/`auto_approve` 미포함**).
3. `sc.exe`로 Windows 서비스 `reversproxy-server`를 등록합니다(자동 시작, 실패 시 5s/10s/30s 재시작, reset 86400).
4. 인바운드 TCP 허용 방화벽 규칙을 추가합니다: **8444(data), 8080(http), 8445(https), 9090(admin)**. (제어 평면 `:8443`은 클라이언트 측이므로 서버에 추가하지 않습니다.)
5. 설치 디렉터리를 머신 PATH에 추가합니다.

> 기본 설정에는 `auto_approve`가 없으므로, 서버는 새 클라이언트를 TOFU 승인 대기 상태로 차단합니다. 운영자가 관리 API로 승인하거나 `server.yaml`에 `auto_approve: true`를 추가해야 합니다.

설치 후:

```powershell
notepad "$env:ProgramFiles\reversproxy\server.yaml"   # 설정 편집 (특히 clients[].address)
Start-Service reversproxy-server
Get-Service reversproxy-server
```

### go install

```bash
go install github.com/EcoKG/reversproxy/cmd/server@latest
go install github.com/EcoKG/reversproxy/cmd/client@latest
```

(`cmd/winclient`는 현재 컴파일되지 않으므로 설치할 수 없습니다. Windows에서는 CLI 클라이언트 `cmd/client`를 직접 빌드해 사용하시기 바랍니다.)

### 바이너리 직접 다운로드

[Releases](https://github.com/EcoKG/reversproxy/releases/latest) 페이지에서 위 표의 아티팩트를 내려받아 실행 권한을 부여한 뒤 실행하시면 됩니다.

---

## 빠른 시작

> 설정 파일이 없어도 양쪽 모두 내장 기본값으로 실행됩니다. 다만 운영에서는 토큰과 `clients[].address`를 반드시 지정해야 합니다.

### 1단계: 클라이언트 설정 (`config.yaml`, NAT 뒤의 머신)

```yaml
listen_addr: ":8443"        # 서버가 다이얼해 들어오는 주소 (클라이언트가 리슨)
auth_token: "supersecret"   # 서버와 공유하는 사전 공유 토큰
name: "client1"             # 서버 clients[].name 과 일치해야 함 (TOFU 키)
insecure: false             # 운영에서는 false 권장
log_level: "info"

# 외부에 노출할 로컬 서비스(터널)
tunnels:
  - type: tcp
    local_host: "127.0.0.1"
    local_port: 8080
    requested_port: 0        # 0 = 서버가 임의 공개 포트 할당
  - type: http
    hostname: "app.example.com"
    local_host: "127.0.0.1"
    local_port: 3000

# 클라이언트 측 출구 프록시
socks_addr: ":1080"          # 빈 문자열이면 비활성화
http_proxy_addr: ":8080"     # 빈 문자열이면 비활성화

# 정적 TCP 포트 포워드 (서버를 경유해 원격 대상으로)
port_forwards:
  - local_port: 13389
    remote_host: "192.168.0.5"
    remote_port: 3389
    bind: "0.0.0.0"
```

### 2단계: 서버 설정 (`config.yaml`, 공인 머신)

```yaml
# data_addr:  ":8444"   # 기본값 (생략 가능)
# http_addr:  ":8080"
# https_addr: ":8445"
# admin_addr: ":9090"
auth_token: "supersecret"         # 클라이언트가 자체 토큰을 안 가지면 사용되는 기본 토큰
admin_token: "admin-bearer-token" # 빈 문자열이면 관리 API 무인증
insecure: false                   # 운영에서는 false 권장
log_level: "info"
known_hosts_path: "known_hosts.yaml"
auto_approve: false               # true 이면 TOFU 승인 생략(개발용)
max_conn_rate: 0                  # per-IP conn/sec, 0 = 무제한
max_conn_burst: 10
max_concurrent: 0                 # 전역 동시 연결 상한, 0 = 무제한
min_port: 1

# 서버가 다이얼해 들어갈 클라이언트 목록
clients:
  - name: "client1"               # 클라이언트 name 과 일치 (TOFU 키)
    address: "10.0.0.1:8443"      # 클라이언트의 listen_addr
    auth_token: "supersecret"     # 비우면 위 auth_token 사용
```

### 3단계: 실행

클라이언트(먼저 기동하여 대기):

```bash
./reversproxy-client --config config.yaml
# 콘솔: "Client listening on :8443 ... waiting for proxy server to connect"
```

서버(클라이언트로 다이얼):

```bash
./reversproxy-server --config config.yaml
```

`auto_approve`가 꺼져 있고 클라이언트가 미등록이면 서버는 승인 대기 상태로 차단됩니다. 대시보드(`http://127.0.0.1:9090`)나 다음 명령으로 승인합니다:

```bash
curl -X POST "http://127.0.0.1:9090/api/decide?name=client1&action=approve"
```

---

## 설정 레퍼런스

설정 로더는 `yaml.KnownFields(true)`를 사용하므로 **알 수 없는 YAML 키는 파싱 오류로 기동을 거부**합니다(서버 `os.Exit(1)`, 클라이언트 로그 후 반환). 설정 파일이 **없는 것은 오류가 아니며** 기본값으로 동작합니다.

### 서버 설정 (ServerConfig)

| 키 | 타입 | 기본값 | 설명 |
|----|------|--------|------|
| `data_addr` | string | `:8444` | TCP 데이터 연결 리스너 |
| `http_addr` | string | `:8080` | HTTP 호스트 기반 프록시; 빈 문자열이면 비활성화 |
| `https_addr` | string | `:8445` | HTTPS SNI 라우팅 프록시; 빈 문자열이면 비활성화 |
| `admin_addr` | string | `:9090` | 관리 HTTP API/대시보드; 빈 문자열이면 비활성화 (호스트 생략 시 `127.0.0.1`로 바인딩) |
| `auth_token` | string | `changeme` | 기본 사전 공유 토큰. 클라이언트별 토큰이 없을 때 폴백 |
| `admin_token` | string | `""` | 관리 API용 Bearer 토큰. 빈 문자열이면 무인증 |
| `cert_path` | string | `server.crt` | 서버 TLS 인증서 (없으면 자동 생성; 다이얼 시에는 현재 미사용) |
| `key_path` | string | `server.key` | 서버 TLS 키 |
| `insecure` | bool | `true` | 클라이언트로 다이얼 시 TLS 검증 생략 (개발용) |
| `client_ca_cert` | string | `""` | `insecure: false`일 때 클라이언트 인증서 검증용 CA |
| `tls_fingerprint` | string | `""` | ⚠ **미사용** — 파싱만 되고 코드에서 읽지 않음. 인증서 핀닝은 TOFU(`known_hosts.yaml`, 클라이언트 이름 기준)로 수행 |
| `min_port` | int | `1` | ⚠ **미사용** — 파싱만 되고 효과 없음. 대상 포트 하한은 데이터 평면 핸들러와 SOCKS 경로에 `1`로 하드코딩됨 |
| `max_conn_rate` | float | `0` | per-IP 연결 속도(conn/sec); 0 = 무제한 |
| `max_conn_burst` | int | `10` (DefaultConfig 값은 0) | per-IP 버스트 크기 |
| `max_concurrent` | int64 | `0` | 전역 동시 프록시 연결 상한; 0 = 무제한 |
| `log_level` | string | `info` | `debug`/`info`/`warn`/`error` |
| `known_hosts_path` | string | `known_hosts.yaml` | TOFU 지문 저장소 |
| `auto_approve` | bool | `false` | TOFU 프롬프트 생략, 모든 클라이언트 신뢰 (개발용) |
| `clients` | list | `nil` | 서버가 다이얼할 클라이언트 목록 (아래 참조) |

`clients[]` 항목 구조:

| 키 | 타입 | 설명 |
|----|------|------|
| `name` | string | 사람이 읽는 라벨이자 **TOFU 키** |
| `address` | string | 클라이언트가 리슨하는 `host:port` (서버가 이 주소로 다이얼) |
| `auth_token` | string | 클라이언트별 토큰; 비우면 서버 `auth_token` 사용 |

> 레이트 리미터는 `max_conn_rate > 0` 또는 `max_concurrent > 0`일 때만 생성되며, **HTTP/HTTPS 프록시 accept 루프에만 적용**됩니다. raw TCP 공개 리스너, 데이터 리스너, SOCKS 경로에는 적용되지 않습니다.

### 클라이언트 설정 (ClientConfig)

| 키 | 타입 | 기본값 | 설명 |
|----|------|--------|------|
| `listen_addr` | string | `:8443` | 서버 접속을 수용하는 TLS 리스너 주소 |
| `auth_token` | string | `changeme` | 서버 핸드셰이크에서 검증할 토큰 |
| `name` | string | `client1` | 클라이언트 라벨 (서버 `clients[].name`과 일치) |
| `insecure` | bool | `true` | 다이얼해 들어오는 서버의 TLS 검증 생략 (개발용) |
| `tunnels` | list | `nil` | 등록할 터널 목록 (아래 참조) |
| `log_level` | string | `info` | `debug`/`info`/`warn`/`error` |
| `cert_path` | string | `client.crt` | 클라이언트 리스너 TLS 인증서 (없으면 자동 생성) |
| `key_path` | string | `client.key` | 클라이언트 리스너 TLS 키 |
| `socks_addr` | string | `:1080` | 로컬 SOCKS5 리스너; 빈 문자열이면 비활성화 |
| `socks_user` | string | `""` | SOCKS5 인증 사용자명; 비우면 무인증 |
| `socks_pass` | string | `""` | SOCKS5 인증 비밀번호; 비우면 무인증 |
| `http_proxy_addr` | string | `:8080` | 로컬 HTTP CONNECT 프록시; 빈 문자열이면 비활성화 |
| `port_forwards` | list | `nil` | 정적 TCP 포트 포워드 (아래 참조) |

`tunnels[]` 항목 구조:

| 키 | 타입 | 설명 |
|----|------|------|
| `type` | string | `tcp` / `http` / `https` (빈 문자열은 `tcp`로 처리) |
| `local_host` | string | 로컬 서비스 호스트명 (예: `127.0.0.1`) |
| `local_port` | int | 로컬 서비스 포트 |
| `requested_port` | int | 서버에 요청할 공개 포트; `0` = 임의 |
| `hostname` | string | `http`/`https` 타입의 가상 호스트명 |

`port_forwards[]` 항목 구조:

| 키 | 타입 | 설명 |
|----|------|------|
| `local_port` | int | 로컬 리슨 포트 |
| `remote_host` | string | 서버가 다이얼할 대상 호스트 |
| `remote_port` | int | 대상 포트 |
| `bind` | string | 로컬 바인드 주소. (YAML 주석은 `0.0.0.0`이라 하나, 빈 값일 때 **런타임 기본값은 `127.0.0.1`**) |

> SOCKS 설정과 레이트 리밋은 별도의 중첩 블록이 아니라 **평면(flat) 키**입니다(`socks_addr` 등은 클라이언트, `max_conn_*`는 서버). 과거에 서버에 있던 SOCKS 설정은 모두 클라이언트로 이동했습니다.

### 컴파일 타임 상수 (설정 불가)

`internal/config/defaults.go`의 다음 값은 YAML/플래그로 변경할 수 없습니다:

| 상수 | 값 |
|------|-----|
| `HandshakeTimeout` | 10s |
| `MessageReadTimeout` | 45s |
| `HeartbeatInterval` | 10s |
| `HeartbeatStaleThreshold` | 30s |
| `PongTimeout` | 10s |
| `DataConnWaitTimeout` | 15s |
| `SOCKSDialTimeout` | 15s |
| `SOCKSReadyTimeout` | 30s |
| `SOCKSHandshakeTimeout` | 30s |
| `TCPKeepAlivePeriod` | 15s |
| `ProxyReadTimeout` | 10s |
| `RelayBufSize` | 32768 (32KB) |
| `MuxChannelBuffer` | 64 |

또한 TLS 최소 버전은 양쪽 모두 **TLS 1.3으로 하드코딩**되어 있어 변경할 수 없습니다.

---

## 포트 목록

| 포트 | 측 | 역할 | 설정 키 |
|------|----|------|---------|
| `:8443` | 클라이언트(리슨) | 제어 평면. **서버가 이 포트로 다이얼** | `listen_addr` |
| `:8444` | 서버(리슨) | 데이터 연결 리스너 (클라이언트가 외부 연결마다 다이얼백) | `data_addr` |
| `:8080` | 서버(리슨) | HTTP 호스트 기반 프록시 (공개) | `http_addr` |
| `:8445` | 서버(리슨) | HTTPS SNI 라우팅 프록시 (공개, TLS 미종료) | `https_addr` |
| `:9090` | 서버(리슨) | 관리 API + 대시보드 (**기본 `127.0.0.1` 전용**) | `admin_addr` |
| `:1080` | 클라이언트(리슨) | 로컬 SOCKS5 프록시 | `socks_addr` |
| `:8080` | 클라이언트(리슨) | 로컬 HTTP CONNECT 프록시 (서버 `http_addr`와 번호만 같고 다른 머신) | `http_proxy_addr` |
| `<requested_port>` | 서버(리슨) | 터널별 공개 TCP 포트 (요청 시 동적 개방) | `tunnels[].requested_port` |
| `<local_port>` | 클라이언트(리슨) | 포트 포워드 로컬 리스너 | `port_forwards[].local_port` |

> 고정된 `:9000` 공개 포트는 없습니다. 공개 TCP 포트는 터널별 `requested_port`(0이면 OS 할당)로 동적 결정됩니다.

---

## CLI 플래그

플래그는 **명령줄에서 명시적으로 지정한 경우에만** 설정 파일 값을 덮어씁니다(`flag.Visit`). 따라서 지정하지 않은 플래그는 설정/기본값을 유지합니다.

### 클라이언트 (`cmd/client`)

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--config` | `config.yaml` | YAML 설정 파일 경로 |
| `--listen` | (config `:8443`) | 서버 접속 수용용 TLS 리슨 주소 |
| `--token` | (config `changeme`) | 사전 공유 인증 토큰 |
| `--name` | (config `client1`) | 클라이언트 라벨 |
| `--insecure` | `false` | 서버 TLS 인증서 검증 생략 |
| `--local-host` | `127.0.0.1` | 터널링할 로컬 서비스 호스트 (레거시 단일 터널) |
| `--local-port` | `0` | 로컬 서비스 포트; 0 = 터널 없음 (레거시) |
| `--pub-port` | `0` | 서버에 요청할 공개 포트; 0 = 임의 (레거시) |
| `--http-host` | `""` | HTTP 호스트 라우팅용 호스트명 (레거시) |
| `--http-port` | `0` | HTTP 라우팅용 로컬 포트 (레거시) |
| `--https-host` | `""` | HTTPS SNI 라우팅용 호스트명 (레거시) |
| `--https-port` | `0` | HTTPS 라우팅용 로컬 포트 (레거시) |
| `--socks-addr` | (config `:1080`) | 로컬 SOCKS5 리스너 주소 |
| `--socks-user` | `""` | SOCKS5 인증 사용자명; 비우면 무인증 |
| `--socks-pass` | `""` | SOCKS5 인증 비밀번호; 비우면 무인증 |
| `--http-proxy-addr` | (config `:8080`) | 로컬 HTTP CONNECT 프록시 주소 |
| `--log-level` | (config `info`) | `debug`/`info`/`warn`/`error` |
| `--cert` | (config `client.crt`) | TLS 인증서 파일 경로 |
| `--key` | (config `client.key`) | TLS 키 파일 경로 |

> 레거시 단일 터널 플래그는 설정 파일의 `tunnels[]` 뒤에 **추가**됩니다. TCP 터널은 `--local-port > 0`일 때, HTTP/HTTPS 터널은 host와 port 플래그가 모두 설정될 때만 추가됩니다. 포트 포워드에는 CLI 플래그가 없으며 설정 파일로만 지정합니다.

### 서버 (`cmd/server`)

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--config` | `config.yaml` | YAML 설정 파일 경로 |
| `--data-addr` | (config `:8444`) | TCP 데이터 연결 리슨 주소 |
| `--http-addr` | (config `:8080`) | HTTP 호스트 기반 프록시 리슨 주소 |
| `--https-addr` | (config `:8445`) | HTTPS SNI 프록시 리슨 주소 |
| `--admin-addr` | (config `:9090`) | 관리 HTTP API 리슨 주소 |
| `--token` | (config `changeme`) | 기본 사전 공유 인증 토큰 |
| `--cert` | (config `server.crt`) | TLS 인증서 파일 경로 |
| `--key` | (config `server.key`) | TLS 키 파일 경로 |
| `--log-level` | (config `info`) | `debug`/`info`/`warn`/`error` |

> 존재하지 않는 플래그: `--addr`, `--server`, `--gencert`, `--auto-approve`, `--admin-token`, 서버 `--insecure`/`--socks*`. 이들 중 `auto_approve`/`admin_token`/`insecure`는 설정 파일 전용 키이며, 인증서는 기동 시 `control.LoadOrGenerateCert`로 자동 생성됩니다.

---

## 관리 API & 웹 대시보드

서버 바이너리에 임베드된 단일 HTTP 서버가 `admin_addr`(기본 `:9090`)에서 대시보드와 JSON API, SSE를 모두 제공합니다.

> **바인딩 주의**: 호스트를 생략한 `:9090` 같은 주소는 자동으로 `127.0.0.1:9090`(로컬 전용)으로 재작성됩니다. 외부에서 접근하려면 `admin_addr`에 명시적 호스트(예: `0.0.0.0:9090`)를 지정해야 합니다.
>
> **인증 주의**: 인증은 `admin_token`(Bearer) 기반이며 **기본값이 빈 문자열이라 무인증**입니다. 또한 인증은 `/api/*`와 `/static`에만 적용되고 대시보드 HTML(`/`)은 의도적으로 무인증입니다.

### REST 엔드포인트 (총 8개)

| 메서드 | 경로 | 설명 |
|--------|------|------|
| GET | `/api/clients` | 접속된 클라이언트 목록 `{clients:[{id,name,addr,connected_at}]}` |
| GET | `/api/tunnels` | 활성 터널 `{tunnels:[{id,client_id,type:tcp\|http\|https,local_addr,public_addr?,hostname?}]}` |
| GET | `/api/stats` | 트래픽 통계 `{total_connections,active_connections,bytes_in,bytes_out,tunnels{...}}` |
| GET | `/api/pending` | TOFU 승인 대기 목록 `{pending:[{client_name,addr,fingerprint,requested_at}]}` |
| GET | `/api/known-hosts` | 신뢰된 지문 목록 `{hosts:[{Name,Fingerprint}]}` (키가 대문자임에 유의) |
| DELETE | `/api/known-hosts?name=<client>` | 신뢰 지문 철회 → 다음 다이얼 시 재승인 필요 `{ok:true}` |
| POST | `/api/decide?name=<client>&action=approve\|reject` | TOFU 승인/거부 (400 잘못된 파라미터, 404 대기 없음, 503 승인 비활성) `{ok:true}` |
| POST | `/api/reconnect?name=<client>` | 일치 연결을 끊어 서버가 재다이얼하게 함 `{ok:true,disconnected:<n>}` |
| GET | `/api/events` | SSE 이벤트 스트림 (`text/event-stream`) |

> 액션 파라미터는 **쿼리 스트링**으로 전달하며 요청 본문은 비어 있습니다(POST/DELETE 모두). `Authorization: Bearer <admin_token>` 헤더는 `admin_token`이 설정된 경우에만 필요합니다.

### SSE 이벤트 스트림 (`/api/events`)

- `Content-Type: text/event-stream`, 이벤트는 `data: <json>\n\n`, 15초마다 `: ping` 하트비트 주석을 보냅니다.
- 이벤트 타입: `client.connected`, `client.disconnected`, `client.pending`, `client.approved`, `client.rejected`, `client.fingerprint_mismatch`.
- EventBus는 best-effort 비차단 팬아웃이며, 구독자 버퍼가 가득 차면 이벤트를 드롭합니다(차단하지 않음).

### 대시보드 (`/`)

- 임베드된 정적 자산(`/static/style.css`, `/static/app.js`)으로 구성된 단일 페이지입니다.
- 2초마다 `/api/clients`, `/api/tunnels`, `/api/stats`, `/api/pending`, `/api/known-hosts`를 폴링하고 `/api/events`에 EventSource로 연결합니다.
- 대시보드 액션: 대기 클라이언트 **승인/거부**, 클라이언트 **강제 재연결**, 신뢰 호스트 **철회**.

### 정적 IP/공인망에서의 사용 예

```bash
# admin_addr 에 명시 호스트를 지정한 경우에만 외부 접근 가능
curl http://<서버IP>:9090/api/clients
curl -X POST "http://<서버IP>:9090/api/decide?name=client1&action=approve"
curl -H "Authorization: Bearer <admin_token>" http://<서버IP>:9090/api/stats   # admin_token 설정 시
```

---

## SOCKS5 / HTTP CONNECT / 포트 포워드 (클라이언트 측 출구 프록시)

이 기능들의 리스너는 모두 **클라이언트** 측에 있으며, 서버는 인터넷 출구 노드로서 대상을 직접 다이얼합니다(DNS도 서버 측 해석). 데이터는 별도 데이터 포트 없이 단일 제어 연결 위에서 멀티플렉싱됩니다.

### SOCKS5 (`socks_addr`, 기본 `:1080`)

- **CONNECT(0x01) 전용**입니다. BIND(0x02)와 UDP ASSOCIATE는 **지원하지 않습니다**(0x07 반환).
- 주소 타입 IPv4 / DOMAIN / IPv6를 모두 파싱합니다.
- 인증: NO-AUTH(0x00)와 USERNAME/PASSWORD(0x02, RFC 1929). `socks_user`와 `socks_pass`가 **둘 다 설정된 경우에만** 인증을 요구하며, 자격 비교는 `crypto/subtle` 상수 시간 비교를 사용합니다.

사용 예:

```bash
export ALL_PROXY=socks5h://127.0.0.1:1080
curl https://httpbin.org/ip
```

### HTTP CONNECT (`http_proxy_addr`, 기본 `:8080`)

- **인증이 없습니다**(SOCKS5만 인증 지원). SOCKS mux를 그대로 재사용합니다.
- CONNECT 메서드는 `200 Connection Established` 후 raw 바이트를 터널링합니다. 비-CONNECT 요청에는 `HTTPS_PROXY` 설정을 안내하는 200 안내 페이지를 반환합니다.

사용 예:

```bash
export HTTPS_PROXY=http://127.0.0.1:8080
curl https://httpbin.org/ip
```

### 정적 포트 포워드 (`port_forwards`)

- 로컬 포트를 서버를 경유해 정적 원격 대상으로 연결합니다. 로컬 측에 SOCKS 핸드셰이크가 없고 raw 바이트로 처리됩니다.
- 예: 로컬 `:13389` → 서버 → `192.168.0.5:3389`(RDP). **CLI 플래그가 없고 설정 파일 전용**입니다.

> 멀티 서버: 세 프런트엔드 모두 단일 `ServerPool`을 공유하고 라운드로빈으로 세션을 선택합니다. 풀이 비어 있으면 SOCKS는 general-failure(0x01), HTTP CONNECT는 503, 포트 포워드는 조용히 연결을 닫습니다. 이 경로의 허용 포트는 `min_port` 설정과 무관하게 `[1, 65535]`로 하드코딩되어 있습니다.

---

## 빌드

### 로컬 빌드

```bash
go build -o reversproxy-server ./cmd/server   # 정상 빌드
go build -o reversproxy-client ./cmd/client   # 정상 빌드
```

> 주의: `go build ./...`는 Windows에서 `cmd/winclient` 컴파일 실패로 함께 실패합니다(아래 winclient 경고 참고). 동작하는 진입점은 `cmd/server`와 `cmd/client` 두 개입니다.

### 크로스 컴파일

```bash
# Linux 서버 (arm64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o reversproxy-server-linux-arm64 ./cmd/server

# Windows 서버 (CLI, amd64)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o reversproxy-server-windows-amd64.exe ./cmd/server

# Windows 클라이언트 (systray GUI) — ⚠ 현재 컴파일되지 않음
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
  go build -ldflags "-H windowsgui -s -w" -o reversproxy-client-windows-amd64.exe ./cmd/winclient
```

> ⚠ **`cmd/winclient`는 현재 컴파일되지 않습니다.** 멀티 서버 리팩토링(`ServerPool`/`ServerSession` 도입) 이후 `socks.StartClientSOCKSProxy`/`StartHTTPConnectProxy`/`StartPortForward`와 `client.HandleServerConn`의 시그니처가 바뀌었으나 `cmd/winclient/main.go`가 옛 시그니처로 호출하고 있어 빌드가 실패합니다(서버·CLI 클라이언트는 정상 빌드). 빌드하려면 winclient를 새 `ServerPool` API에 맞게 먼저 수정해야 합니다. systray GUI 빌드 자체는 `CGO_ENABLED=1`과 `-H windowsgui`, 크로스 컴파일 시 `x86_64-w64-mingw32-gcc`(MinGW)를 요구합니다.

### Makefile 타깃

```bash
make build    # go build ./...
make test     # go test -race ./...
make lint     # go vet ./...
```

> **경고**: 현재 `Makefile`의 `run-server`(`-addr`/`-token`), `run-client`(`-server`), `cert`(`-gencert`) 타깃은 **실제 CLI와 맞지 않아 "flag provided but not defined" 오류로 실패**합니다(서버에는 `-addr`/`-gencert`가 없고, 클라이언트는 다이얼하지 않으므로 `-server`가 없습니다). 직접 실행 시 위의 `go run`/`go build` 명령과 본문의 CLI 플래그를 사용하시기 바랍니다.

---

## 프로젝트 구조

모듈 경로: `github.com/EcoKG/reversproxy` (Go 1.22.10). 직접 의존성: `getlantern/systray`(winclient 전용), `google/uuid`, `gopkg.in/yaml.v3`.

```
cmd/
  client/        CLI 클라이언트 진입점 (tls.Listen, ServerPool, 출구 프런트엔드 기동)
  server/        서버 진입점 (dialClientLoop, tofuCheck, 공개 리스너, 관리 서버)
  winclient/     Windows 시스템 트레이 GUI 클라이언트 (//go:build windows, CGO 필요) — ⚠ 현재 미컴파일(stale)
internal/
  config/        YAML 설정 로딩 (KnownFields 엄격), 기본값, 컴파일 타임 상수
  control/       제어 평면: 핸드셰이크, 하트비트, TOFU 승인(approval), known_hosts, TLS, 레지스트리
  client/        클라이언트 측 세션 처리: HandleServerConn, ServerPool, ConnWriter
  protocol/      와이어 프로토콜: 길이 접두 + gob Envelope 프레이밍, 메시지 타입, 대상 검증
  tunnel/        데이터 평면: 데이터/공개/HTTP/HTTPS 리스너, 릴레이, SOCKS mux, 레이트 리밋
  socks/         클라이언트 측 SOCKS5 / HTTP CONNECT / 포트 포워드 프런트엔드 (server.go는 레거시)
  admin/         관리 HTTP 서버, REST API, 임베드 UI(ui/), SSE EventBus
  stats/         락-프리 원자 카운터 (CountedReader/Writer)
  logger/        JSON slog 로거 (component 태그)
  reconnect/     지수 백오프 + 재연결 설정 헬퍼
  proxy/         빈 플레이스홀더 (stub.go — 미구현)
  app/           ServerApp 추상화 (테스트 전용, 운영 서버는 cmd/server 사용)
scripts/         install-client.sh, install-server.ps1, run-client.sh, patch-client.sh
.github/workflows/release.yml   태그 푸시 시 6종 아티팩트 크로스 컴파일 + 릴리스
```

> 참고: `cmd/winclient`는 멀티 서버 리팩토링 이후 옛 함수 시그니처를 호출하여 **현재 컴파일되지 않는 stale 코드**입니다(동작하는 진입점은 `cmd/server`와 `cmd/client`뿐). `internal/proxy/stub.go`는 미구현 빈 스텁이며 실제 데이터 평면은 `internal/tunnel`에 있습니다. `internal/app`의 `ServerApp`는 테스트 전용으로, TOFU/EventBus/승인 배선이 빠져 있어 운영 진입점이 아닙니다. `internal/socks/server.go`(서버 측 SOCKS)는 더 이상 `cmd/server`에서 사용되지 않는 레거시 코드입니다.

---

## 라이선스

MIT License.
