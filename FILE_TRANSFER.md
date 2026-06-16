# 파일 전송 (우클릭 / 드롭 폴더)

기존 reverse-proxy 터널을 전송로로 재사용해 **두 거점 간 파일을 양방향으로** 주고받는 기능입니다.
새 와이어 프로토콜 없이, 각 피어가 로컬에 작은 HTTP 수신 엔드포인트를 띄우고 그것을 터널로
노출하는 방식이며, 전송은 **청크 업로드 + SHA-256 무결성 검증 + 중간 끊김 시 재개**를 지원합니다.

- **윈도우**: 탐색기에서 파일 우클릭 → "Reversproxy로 파일 전송" → 상대 거점의 드롭 폴더로 전송, 트레이 알림.
- **리눅스/CLI**: `reversproxy-client send` 서브커맨드로 전송, 수신은 드롭 폴더에 자동 저장.

> 디렉터리(폴더) 전송은 아직 미지원입니다 — 먼저 zip으로 묶어 단일 파일로 보내십시오.

---

## 1. 동작 원리와 배선

토폴로지상 **서버가 클라이언트로 접속**하고(클라이언트가 listen), 클라이언트의 로컬 서비스는
서버의 공개 포트로 노출되며, 클라이언트는 포트포워드로 서버 너머로 나갈 수 있습니다. 파일 수신
엔드포인트(`receive_addr`)도 같은 방식으로 상대에게 노출합니다.

```
[클라이언트 거점]                                  [서버 거점]
 수신기 127.0.0.1:8089  ◄── tunnel(pub 18089) ◄──  send_endpoint http://127.0.0.1:18089
 send_endpoint                                     수신기 127.0.0.1:8089
   http://127.0.0.1:8090 ── port-forward ─────────►  (서버가 127.0.0.1:8089 로 dial)
```

- **서버 → 클라이언트**: 클라이언트가 자신의 수신기(8089)를 `tunnel`로 공개 포트(18089)에
  노출 → 서버는 자기 쪽 `http://127.0.0.1:18089`로 보냄.
- **클라이언트 → 서버**: 클라이언트가 `port_forward`로 서버의 수신기(8089)에 닿는 로컬 포트
  (8090)를 열고 → `http://127.0.0.1:8090`으로 보냄.

---

## 2. 설정 예시

### 클라이언트 `config.yaml`

```yaml
listen_addr: "0.0.0.0:8443"
auth_token: "change-this"
name: "site-a"
insecure: false

# (서버→클라이언트용) 수신기를 공개 포트 18089 로 노출
tunnels:
  - type: tcp
    local_host: "127.0.0.1"
    local_port: 8089          # = file_transfer.receive_addr 의 포트
    requested_port: 18089

# (클라이언트→서버용) 서버의 수신기(127.0.0.1:8089)로 닿는 로컬 포트
port_forwards:
  - local_port: 8090
    remote_host: "127.0.0.1"
    remote_port: 8089
    bind: "127.0.0.1"

file_transfer:
  enabled: true
  receive_addr: "127.0.0.1:8089"
  drop_dir: "received"               # 받은 파일이 저장되는 폴더 (상대경로는 exe 기준)
  token: "ft-shared-secret"          # 양쪽 동일하게 설정 권장
  send_endpoint: "http://127.0.0.1:8090"   # 클라이언트가 보낼 대상(서버 수신기)
  control_addr: "127.0.0.1:8077"     # 윈도우 우클릭 헬퍼가 트레이에 넘길 로컬 IPC
  max_file_size: 0                   # 0 = 무제한
```

### 서버 `config.yaml`

```yaml
data_addr:  "0.0.0.0:8444"
auth_token: "change-this"
clients:
  - name: "site-a"
    address: "site-a-host:8443"
    auth_token: "change-this"

file_transfer:
  enabled: true
  receive_addr: "127.0.0.1:8089"
  drop_dir: "received"
  token: "ft-shared-secret"
  send_endpoint: "http://127.0.0.1:18089"  # 서버가 보낼 대상(클라이언트 수신기, 공개 포트)
  max_file_size: 0
```

> `token`은 터널의 TLS·인증 토큰에 더해진 업로드용 공유 비밀입니다. 양쪽이 같아야 하며,
> 비워 두면 업로드 인증을 하지 않습니다(신뢰된 망에서만 권장).

---

## 3. 윈도우 우클릭 사용법

1. `reversproxy-client.exe`(GUI 트레이)를 실행합니다. 위 `file_transfer` 설정이 있으면
   수신기와 로컬 IPC가 자동으로 뜹니다.
2. 트레이 아이콘 → **"우클릭 메뉴 등록"** 클릭 (현재 사용자 레지스트리에만 등록 — 관리자 권한 불필요).
   - Windows 11에서는 우클릭 후 **"추가 옵션 표시"** 안에 "Reversproxy로 파일 전송"이 나타납니다.
3. 탐색기에서 파일 우클릭 → **"Reversproxy로 파일 전송"** → 상대 거점의 드롭 폴더로 전송됩니다.
4. 받은 파일은 트레이 → **"수신함 열기"**로 확인합니다. 도착 시 트레이 툴팁으로 알립니다.
5. 제거하려면 트레이 → **"우클릭 메뉴 해제"**.

---

## 4. CLI 사용법 (리눅스/윈도우 공통)

수신은 `file_transfer.enabled: true`면 서버·클라이언트 모두 자동입니다. 전송은:

```bash
# 상대 거점의 수신 엔드포인트(터널 너머)를 -to 로 지정
reversproxy-client send -to http://127.0.0.1:8090 -token ft-shared-secret ./report.pdf
```

`-to`는 이 호스트에서 **터널을 통해 상대 수신기에 닿는 주소**입니다(위 배선의 `send_endpoint`와 동일).

---

## 5. 보안 메모

- 수신기는 기본적으로 **루프백(127.0.0.1)**에만 바인딩하고 터널을 통해서만 상대에게 노출됩니다.
  외부에 직접 열지 마십시오.
- `token`을 반드시 설정하고, 운영에서는 `insecure: false` + 정상 인증서를 사용하십시오.
- 파일명은 수신 측에서 경로 요소를 제거(base name)해 드롭 폴더 밖으로 못 나가게 처리합니다.
- 같은 이름이 도착하면 덮어쓰지 않고 ` (1)`, ` (2)` …를 붙여 저장합니다.
