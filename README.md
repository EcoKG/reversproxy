# reversproxy

NAT/방화벽 뒤의 서비스를 외부에 노출하는 **리버스 터널 프록시**.

일반 리버스 프록시(ngrok 등)와 달리, **클라이언트가 리슨하고 서버가 연결**하는 구조입니다.

```
                       ┌─────────────────────────────────────────┐
                       │              Proxy Server               │
  External User ──TCP──▶  :9000 (public) ──tunnel──▶            │
  Browser ──HTTP──────▶  :8080 (HTTP)    ──tunnel──▶  TLS Dial ─┼──▶ Client A (:8443) ──▶ Local :80
  Browser ──HTTPS─────▶  :8445 (HTTPS)   ──tunnel──▶  TLS Dial ─┼──▶ Client B (:8443) ──▶ Local :443
                       │  :9090 (Admin API)                      │
                       └─────────────────────────────────────────┘
```

---

## 설치

### Linux 클라이언트 — 한 줄 설치

```bash
curl -fsSL https://raw.githubusercontent.com/EcoKG/reversproxy/master/scripts/install-client.sh | sudo bash
```

자동으로 처리되는 것:
- `/usr/local/bin/reversproxy-client` 바이너리 설치
- `/etc/reversproxy/client.yaml` 기본 설정 파일 생성
- `reversproxy-client` systemd 서비스 등록 (자동 시작, 재시작)

설치 후:
```bash
sudo nano /etc/reversproxy/client.yaml     # 설정 편집
sudo systemctl start reversproxy-client    # 시작
sudo systemctl status reversproxy-client   # 상태 확인
journalctl -u reversproxy-client -f        # 실시간 로그
```

### Windows 서버 — PowerShell 설치

PowerShell을 **관리자 권한**으로 실행한 뒤:

```powershell
irm https://raw.githubusercontent.com/EcoKG/reversproxy/master/scripts/install-server.ps1 | iex
```

자동으로 처리되는 것:
- `C:\Program Files\reversproxy\` 에 바이너리 + 설정 파일 설치
- `reversproxy-server` Windows 서비스 등록 (자동 시작, 실패 시 자동 재시작)
- 방화벽 규칙 추가 (8080, 8444, 8445, 9090 포트)
- 시스템 PATH에 추가

설치 후:
```powershell
notepad "C:\Program Files\reversproxy\server.yaml"   # 설정 편집
Start-Service reversproxy-server                      # 시작
Get-Service reversproxy-server                        # 상태 확인
```

### Go 소스에서 설치

```bash
go install github.com/EcoKG/reversproxy/cmd/client@latest
go install github.com/EcoKG/reversproxy/cmd/server@latest
```

### 바이너리 직접 다운로드

[Releases](https://github.com/EcoKG/reversproxy/releases) 페이지에서 다운로드.

| 파일 | OS | 용도 |
|------|----|------|
| `reversproxy-client-linux-amd64` | Linux x86_64 | 클라이언트 |
| `reversproxy-client-linux-arm64` | Linux ARM64 | 클라이언트 (라즈베리파이 등) |
| `reversproxy-server-windows-amd64.exe` | Windows x86_64 | 서버 |

---

## 빠른 시작

### 1단계: 클라이언트 설정 (Linux)

`/etc/reversproxy/client.yaml` 편집:

```yaml
listen_addr: ":8443"
auth_token: "my-secret-token"
name: "web-server"
log_level: "info"

tunnels:
  # TCP 터널: 서버의 :9000 포트로 들어온 트래픽을 로컬 :80으로 전달
  - type: tcp
    local_host: "127.0.0.1"
    local_port: 80
    requested_port: 9000

  # HTTP 터널: myapp.example.com 요청을 로컬 :8080으로 전달
  - type: http
    hostname: "myapp.example.com"
    local_host: "127.0.0.1"
    local_port: 8080
```

```bash
sudo systemctl start reversproxy-client
```

### 2단계: 서버 설정 (Windows)

`C:\Program Files\reversproxy\server.yaml` 편집:

```yaml
data_addr: ":8444"
http_addr: ":8080"
https_addr: ":8445"
admin_addr: ":9090"
auth_token: "my-secret-token"

clients:
  - name: "web-server"
    address: "123.45.67.89:8443"     # 클라이언트 공인 IP:포트
    auth_token: "my-secret-token"

  - name: "db-server"
    address: "123.45.67.90:8443"
    auth_token: "another-token"
```

```powershell
Start-Service reversproxy-server
```

### 3단계: 접속 확인

```bash
# TCP 터널 확인 (서버 IP의 9000번 포트)
curl http://서버IP:9000

# HTTP 터널 확인 (Host 헤더 기반)
curl -H "Host: myapp.example.com" http://서버IP:8080

# 관리 API
curl http://서버IP:9090/api/clients    # 연결된 클라이언트 목록
curl http://서버IP:9090/api/tunnels    # 활성 터널 목록
curl http://서버IP:9090/api/stats      # 트래픽 통계
```

---

## 동작 원리

```
1. 클라이언트가 TLS 리스너를 열고 대기           (listen :8443)
2. 서버가 설정된 클라이언트 IP로 TLS 연결         (dial → client:8443)
3. 제어 채널 수립 (인증, 터널 등록, 하트비트)
4. 서버가 퍼블릭 포트를 열어 외부 트래픽 수신
5. 외부 요청 → 서버 → 터널 → 클라이언트 → 로컬 서비스
```

연결이 끊기면 서버가 자동으로 재연결을 시도합니다 (지수 백오프: 1초 → 60초).

---

## 기능

| 기능 | 설명 |
|------|------|
| **리버스 연결** | 클라이언트가 리슨, 서버가 다이얼 — NAT/방화벽 우회 |
| **TCP 터널링** | 임의의 TCP 포트를 터널링 |
| **HTTP 라우팅** | Host 헤더 기반으로 올바른 클라이언트에 요청 전달 |
| **HTTPS 라우팅** | TLS SNI 기반 라우팅 (프록시에서 TLS 종료 없음) |
| **자동 재연결** | 지수 백오프 (1s→60s, ±20% 지터) |
| **다중 클라이언트** | 클라이언트별 독립 터널, 장애 격리 |
| **관리 API** | REST API로 클라이언트/터널/통계 조회 |
| **TLS 1.3** | 모든 제어 연결 암호화, 자체 서명 인증서 자동 생성 |
| **YAML 설정** | 파일 기반 설정, CLI 플래그로 오버라이드 가능 |
| **서비스 등록** | Linux: systemd / Windows: Windows Service |

---

## 포트 목록

| 포트 | 용도 | 위치 |
|------|------|------|
| `:8443` | 클라이언트 TLS 리스너 (서버가 여기로 연결) | 클라이언트 |
| `:8444` | 터널 데이터 연결 | 서버 |
| `:8080` | HTTP 호스트 기반 프록시 | 서버 |
| `:8445` | HTTPS SNI 프록시 | 서버 |
| `:9090` | 관리 REST API | 서버 |

---

## CLI 플래그

### 클라이언트

```
--config       설정 파일 경로 (기본: config.yaml)
--listen       리슨 주소 (기본: :8443)
--token        인증 토큰
--name         클라이언트 이름
--insecure     TLS 인증서 검증 무시
--local-host   터널 대상 로컬 호스트 (기본: 127.0.0.1)
--local-port   터널 대상 로컬 포트
--pub-port     요청할 서버측 퍼블릭 포트
--http-host    HTTP 라우팅 호스트명
--http-port    HTTP 라우팅 로컬 포트
--https-host   HTTPS 라우팅 호스트명
--https-port   HTTPS 라우팅 로컬 포트
--cert         TLS 인증서 경로
--key          TLS 키 경로
--log-level    로그 레벨 (debug/info/warn/error)
```

### 서버

```
--config       설정 파일 경로 (기본: config.yaml)
--data-addr    데이터 연결 리슨 주소 (기본: :8444)
--http-addr    HTTP 프록시 주소 (기본: :8080)
--https-addr   HTTPS 프록시 주소 (기본: :8445)
--admin-addr   관리 API 주소 (기본: :9090)
--token        기본 인증 토큰
--cert         TLS 인증서 경로
--key          TLS 키 경로
--log-level    로그 레벨 (debug/info/warn/error)
```

---

## 직접 빌드

```bash
# 네이티브 빌드
go build -o reversproxy-client ./cmd/client
go build -o reversproxy-server ./cmd/server

# 크로스 컴파일
GOOS=linux  GOARCH=amd64 go build -o reversproxy-client-linux  ./cmd/client
GOOS=linux  GOARCH=arm64 go build -o reversproxy-client-arm64  ./cmd/client
GOOS=windows GOARCH=amd64 go build -o reversproxy-server.exe   ./cmd/server
```

---

## License

MIT
