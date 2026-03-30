# 프로젝트 파일 목록 정리

## 프로젝트 개요
**리버스 터널 프록시 (reversproxy)** - NAT/방화벽 뒤의 서비스를 외부에 노출하는 Go 기반 프로젝트

## 📁 디렉토리 구조

### 🔧 핵심 설정 파일
| 파일 | 설명 |
|------|------|
| `go.mod` | Go 모듈 설정 파일 (github.com/EcoKG/reversproxy) |
| `go.sum` | Go 의존성 체크섬 |
| `Makefile` | 빌드 자동화 스크립트 |
| `.gitignore` | Git 무시 파일 목록 |

### 📖 문서 파일
| 파일 | 설명 |
|------|------|
| `README.md` | 프로젝트 메인 문서 (설치, 설정, 사용법) |
| `CLAUDE.md` | Claude AI 관련 문서 |

### 🚀 메인 애플리케이션
| 파일 | 설명 |
|------|------|
| `cmd/client/main.go` | 클라이언트 애플리케이션 진입점 |
| `cmd/server/main.go` | 서버 애플리케이션 진입점 |

### 📦 빌드된 실행 파일
| 파일 | 플랫폼 | 용도 |
|------|-------|------|
| `client` | 현재 시스템 | 클라이언트 실행파일 |
| `server` | 현재 시스템 | 서버 실행파일 |
| `reversproxy-server` | 현재 시스템 | 서버 실행파일 |

### 📁 배포 파일 (`dist/`)
| 파일 | 설명 |
|------|------|
| `reversproxy-client` | 클라이언트 바이너리 |
| `reversproxy-client-linux-amd64` | Linux x86_64 클라이언트 |
| `reversproxy-client-linux-arm64` | Linux ARM64 클라이언트 (라즈베리파이) |
| `reversproxy-server-windows-amd64.exe` | Windows x86_64 서버 |
| `install-client.sh` | Linux 클라이언트 설치 스크립트 |
| `run-client.sh` | 클라이언트 실행 스크립트 |
| `run-client.zip` | 클라이언트 실행 패키지 |
| `rproxy` | 프록시 실행파일 |
| `socat.deb` | socat 패키지 |

### 🔧 스크립트 파일 (`scripts/`)
| 파일 | 설명 |
|------|------|
| `install-client.sh` | Linux 클라이언트 설치 스크립트 |
| `install-server.ps1` | Windows 서버 설치 PowerShell 스크립트 |
| `patch-client.sh` | 클라이언트 패치 스크립트 |
| `run-client.sh` | 클라이언트 실행 스크립트 |

### 📚 내부 라이브러리 (`internal/`)

#### `internal/admin/` - 관리 API
| 파일 | 설명 |
|------|------|
| `api.go` | REST API 엔드포인트 구현 |
| `api_test.go` | API 테스트 |

#### `internal/app/` - 애플리케이션 코어
| 파일 | 설명 |
|------|------|
| `server_app.go` | 서버 애플리케이션 로직 |
| `server_app_test.go` | 서버 애플리케이션 테스트 |

#### `internal/config/` - 설정 관리
| 파일 | 설명 |
|------|------|
| `config.go` | YAML 설정 파서 |
| `config_test.go` | 설정 테스트 |
| `defaults.go` | 기본값 정의 |

#### `internal/control/` - 제어 채널
| 파일 | 설명 |
|------|------|
| `handler.go` | 제어 메시지 핸들러 |
| `heartbeat.go` | 하트비트 구현 |
| `integration_test.go` | 통합 테스트 |
| `registry.go` | 클라이언트 레지스트리 |
| `registry_test.go` | 레지스트리 테스트 |
| `tls.go` | TLS 설정 관리 |

#### `internal/logger/` - 로깅
| 파일 | 설명 |
|------|------|
| `logger.go` | 로그 설정 및 관리 |

#### `internal/protocol/` - 프로토콜 구현
| 파일 | 설명 |
|------|------|
| `framing.go` | 프레임 처리 |
| `framing_test.go` | 프레임 테스트 |
| `messages.go` | 프로토콜 메시지 정의 |
| `validation.go` | 메시지 검증 |

#### `internal/proxy/` - 프록시 구현
| 파일 | 설명 |
|------|------|
| `stub.go` | 프록시 스텁 |

#### `internal/reconnect/` - 재연결 로직
| 파일 | 설명 |
|------|------|
| `backoff.go` | 지수 백오프 알고리즘 |
| `config.go` | 재연결 설정 |
| `integration_test.go` | 통합 테스트 |
| `reconnect_test.go` | 재연결 테스트 |

#### `internal/socks/` - SOCKS 프로토콜
| 파일 | 설명 |
|------|------|
| `client.go` | SOCKS 클라이언트 |
| `client_test.go` | 클라이언트 테스트 |
| `httpconnect.go` | HTTP CONNECT 프록시 |
| `httpconnect_test.go` | HTTP CONNECT 테스트 |
| `portforward.go` | 포트 포워딩 |
| `proto.go` | SOCKS 프로토콜 정의 |
| `server.go` | SOCKS 서버 |
| `server_test.go` | 서버 테스트 |

#### `internal/stats/` - 통계
| 파일 | 설명 |
|------|------|
| `stats.go` | 트래픽 통계 수집 |

#### `internal/tunnel/` - 터널 구현
| 파일 | 설명 |
|------|------|
| `client.go` | 터널 클라이언트 |
| `ctrl_registry.go` | 제어 레지스트리 |
| `http_proxy.go` | HTTP 프록시 |
| `http_routing_test.go` | HTTP 라우팅 테스트 |
| `https_proxy.go` | HTTPS 프록시 |
| `manager.go` | 터널 매니저 |
| `multi_client_test.go` | 멀티클라이언트 테스트 |
| `ratelimit.go` | 속도 제한 |
| `relay.go` | 데이터 중계 |
| `server.go` | 터널 서버 |
| `socks_mux.go` | SOCKS 멀티플렉싱 |
| `tunnel_integration_test.go` | 터널 통합 테스트 |

### 🗂️ 레거시 파일 (`oldproxy/`)
| 파일 | 설명 |
|------|------|
| `build.bat` | Windows 빌드 스크립트 |
| `client.cs` | C# 클라이언트 (이전 버전) |
| `client.exe` | C# 클라이언트 실행파일 |
| `client.zip` | 클라이언트 패키지 |
| `client_new.exe` | 새 클라이언트 실행파일 |
| `server.cs` | C# 서버 (이전 버전) |
| `server.exe` | C# 서버 실행파일 |
| `server.py` | Python 서버 (이전 버전) |
| `setup-wsl.sh` | WSL 설정 스크립트 |

### 🔧 시스템 설정 파일
| 파일 | 설명 |
|------|------|
| `.claude/agents/vela.md` | Claude AI 에이전트 설정 |
| `.claude/settings.local.json` | Claude 로컬 설정 |
| `socat_1.8.0.0-4build3_amd64.deb` | socat 패키지 파일 |

## 📊 프로젝트 통계

### 파일 유형별 분류
- **Go 소스 파일**: 34개 (`.go`)
- **테스트 파일**: 15개 (`*_test.go`)
- **실행 파일**: 8개 (바이너리)
- **스크립트 파일**: 7개 (`.sh`, `.ps1`, `.bat`)
- **설정/문서 파일**: 8개 (`.md`, `.yaml`, `.json` 등)
- **레거시 파일**: 9개 (`oldproxy/` 디렉토리)

### 주요 기능별 모듈
1. **터널링**: `internal/tunnel/` (11개 파일)
2. **SOCKS 프로토콜**: `internal/socks/` (7개 파일)
3. **제어 채널**: `internal/control/` (6개 파일)
4. **재연결 로직**: `internal/reconnect/` (4개 파일)
5. **프로토콜 처리**: `internal/protocol/` (4개 파일)
6. **설정 관리**: `internal/config/` (3개 파일)
7. **관리 API**: `internal/admin/` (2개 파일)

### 배포 타겟
- **Linux**: AMD64, ARM64 아키텍처 지원
- **Windows**: AMD64 아키텍처 지원
- **서비스 등록**: systemd (Linux), Windows Service

## 🔗 의존성
- **Go 버전**: 1.22.10
- **외부 의존성**:
  - `github.com/google/uuid v1.6.0` (UUID 생성)
  - `gopkg.in/yaml.v3 v3.0.1` (YAML 파싱)

---

*이 문서는 프로젝트 구조 이해와 파일 탐색을 위해 작성되었습니다.*