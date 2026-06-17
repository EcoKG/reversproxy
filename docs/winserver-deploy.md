# winserver (Windows 트레이 서버) 배포 가이드

`cmd/winserver`는 reversproxy **서버**를 사용자 세션에서 실행하는 Windows 트레이 앱입니다.
트레이 아이콘 + 네이티브 관리 콘솔(lxn/walk) + 설정 다이얼로그를 제공합니다.

## 빌드

```bash
make winserver           # dist/winserver.exe (버전 스탬프 포함)
# 또는 직접:
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags="-H windowsgui -X main.Version=$(git describe --tags --always) -X main.Commit=$(git rev-parse --short HEAD)" \
  -o dist/winserver.exe ./cmd/winserver/
```

순수 Go(CGO 불필요)라 Linux/WSL에서 크로스 컴파일됩니다. **GUI 동작은 Windows에서만 검증 가능합니다.**

## 폴더 배치 (예: `C:\reversproxy\`)

```
winserver.exe
server.yaml          # 첫 실행 시 자동 생성(없으면 템플릿 작성 후 메모장 오픈)
server.crt / server.key  # 자동 생성(자체서명)
server.log           # 로그(5MB 초과 시 시작할 때 server.log.1로 1세대 로테이션)
```

## 실행 방식 — 서비스 ❌ / 사용자 세션 ✅

Windows 서비스(session 0)는 **트레이 아이콘을 띄울 수 없습니다.** 따라서:

- NSSM `reversproxy` 서비스를 쓰던 경우 → **중지 + 수동(demand)으로 전환**하고 winserver를 사용
  ```cmd
  sc stop reversproxy
  sc config reversproxy start= demand
  ```
- winserver를 **로그온 자동 실행** 등록: 트레이 메뉴 → "로그온 시 자동 실행" 체크
  (HKCU\…\Run, 관리자 권한 불필요)
- 서비스와 winserver는 **동시 실행 금지**(같은 포트 바인딩). winserver는 단일 인스턴스 가드(named mutex)로 이중 실행을 막습니다.

## 트레이 메뉴

상태 / 서버 재시작 / 관리 콘솔 열기 / 설정 / 설정 파일 열기 / 로그 파일 열기 / 로그온 자동 실행 / 정보 / 종료

## 관리 콘솔 (웹 아님, 네이티브)

탭: **클라이언트**(이름·주소·접속시각·경과·터널수, 선택 끊기) / **터널**(종류·공개포트/호스트·로컬대상) /
**통계**(총·활성 연결, 수신/송신, 프록시 상태) / **로그**(자동 스크롤). 2초 자동 새로고침.
버튼: 새로고침 / 서버 재시작 / 설정 / Admin URL 복사 / 접속 정보 복사 / 정보 / 닫기.

## 보안 (실 배포 전 필수)

- `auth_token`은 **강한 값**으로(설정 다이얼로그의 "생성" 버튼). 클라이언트와 **동일**해야 연결됨.
- 가능하면 `insecure: false` + `tls_fingerprint`(클라이언트 인증서 SHA-256 핀닝) 또는 `client_ca_cert`.
- 내부망 프록시가 필요 없으면 `allow_private_targets`를 끄면 SSRF 가드가 켜집니다.
- `admin_token`을 설정하면 Admin API에 Bearer 인증이 걸립니다(비우면 loopback 무인증).

## 방화벽

`data_addr`/`http_addr`/`https_addr` 포트의 인바운드 허용 규칙이 필요합니다(외부 노출 시).
`admin_addr`는 loopback 권장.

## 코드 서명(선택, Windows 전용 수동 단계)

```cmd
signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 dist\winserver.exe
```
