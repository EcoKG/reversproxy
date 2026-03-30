# 개발자 가이드

## 프로젝트 구조 이해

### 핵심 아키텍처

```
┌─────────────────┐    TLS 연결    ┌─────────────────┐
│   Proxy Server  │ ◄──────────── │     Client      │
│                 │               │  (NAT/방화벽 뒤) │
│ :8444 (data)    │               │ :8443 (listen)  │
│ :8080 (http)    │               │                 │
│ :8445 (https)   │  터널 데이터    │ ──► Local :80   │
│ :9090 (admin)   │ ──────────────► │ ──► Local :443  │
└─────────────────┘               └─────────────────┘
```

### 데이터 플로우

1. **연결 수립**: 클라이언트가 `:8443`에서 리슨, 서버가 연결
2. **제어 채널**: 인증, 터널 등록, 하트비트 관리
3. **데이터 터널**: 외부 요청 → 서버 → 터널 → 클라이언트 → 로컬

## 개발 환경 설정

### 필수 요구사항
- Go 1.22.10+
- Git

### 로컬 개발 환경
```bash
# 저장소 클론
git clone https://github.com/EcoKG/reversproxy.git
cd reversproxy

# 의존성 설치
go mod download

# 빌드 테스트
make build

# 테스트 실행
go test ./...
```

### 빌드 명령어
```bash
# 네이티브 빌드
go build -o client ./cmd/client
go build -o server ./cmd/server

# 크로스 컴파일
make build-all

# 특정 플랫폼
GOOS=linux GOARCH=amd64 go build -o reversproxy-client ./cmd/client
GOOS=windows GOARCH=amd64 go build -o reversproxy-server.exe ./cmd/server
```

## 코드 구조 가이드

### 핵심 패키지 역할

#### 1. `cmd/` - 애플리케이션 진입점
- **client**: 클라이언트 애플리케이션
- **server**: 서버 애플리케이션

#### 2. `internal/` - 비공개 라이브러리

##### `internal/config/`
- YAML 설정 파일 파싱
- CLI 플래그와 설정 파일 병합
- 기본값 관리

##### `internal/protocol/`
- 프레임 기반 메시지 프로토콜
- 메시지 직렬화/역직렬화
- 프로토콜 검증

##### `internal/control/`
- 클라이언트-서버 제어 채널
- 하트비트 및 연결 상태 관리
- TLS 인증서 관리

##### `internal/tunnel/`
- 터널 생성 및 관리
- HTTP/HTTPS/TCP 프록시
- 트래픽 라우팅

##### `internal/socks/`
- SOCKS5 프로토콜 구현
- HTTP CONNECT 프록시
- 포트 포워딩

##### `internal/reconnect/`
- 자동 재연결 로직
- 지수 백오프 알고리즘
- 연결 실패 처리

## 테스트 가이드

### 테스트 유형
1. **단위 테스트**: `*_test.go`
2. **통합 테스트**: `integration_test.go`

### 테스트 실행
```bash
# 전체 테스트
go test ./...

# 상세 출력
go test -v ./...

# 특정 패키지
go test ./internal/tunnel/

# 커버리지
go test -cover ./...

# 벤치마크
go test -bench=. ./...
```

### 테스트 작성 가이드
- 각 공개 함수에 대한 테스트 작성
- 테이블 드리븐 테스트 패턴 사용
- 에러 케이스 포함
- 예시: `internal/protocol/framing_test.go`

## 기여 가이드

### 코딩 스타일
- `gofmt`로 포맷팅
- `go vet`으로 정적 분석
- `golint`로 스타일 체크

### 커밋 메시지
```
type: brief description

Detailed explanation if needed.

type: feat|fix|docs|style|refactor|test|chore
```

### 풀 리퀘스트
1. 기능 브랜치 생성
2. 테스트 작성 및 통과
3. 문서 업데이트
4. 코드 리뷰 요청

## 디버깅 가이드

### 로그 레벨
```bash
# 클라이언트 디버그
./client --log-level=debug

# 서버 디버그
./server --log-level=debug
```

### 일반적인 문제들

#### 1. 연결 실패
```bash
# 클라이언트가 리슨 중인지 확인
netstat -an | grep 8443

# 서버가 클라이언트에 접근 가능한지 확인
telnet client-ip 8443
```

#### 2. 터널 문제
```bash
# 관리 API로 상태 확인
curl http://server:9090/api/tunnels
curl http://server:9090/api/clients
```

#### 3. TLS 문제
```bash
# TLS 연결 테스트
openssl s_client -connect client-ip:8443
```

## 배포 가이드

### 릴리즈 프로세스
1. 버전 태그 생성
2. 멀티 플랫폼 빌드
3. 바이너리 업로드
4. 설치 스크립트 업데이트

### 자동화된 배포
```bash
# GitHub Actions 또는 CI/CD 파이프라인 사용
# dist/ 디렉토리에 빌드된 바이너리 확인
ls -la dist/
```

## 성능 최적화

### 프로파일링
```bash
# CPU 프로파일링
go test -cpuprofile=cpu.prof -bench=.

# 메모리 프로파일링
go test -memprofile=mem.prof -bench=.

# pprof로 분석
go tool pprof cpu.prof
```

### 모니터링
- 관리 API (`/api/stats`) 활용
- 시스템 리소스 모니터링
- 네트워크 대역폭 체크

---

*이 가이드는 reversproxy 프로젝트 개발을 위한 참고 자료입니다.*