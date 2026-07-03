# 설정 샘플 (examples)

파일 전송을 켠 **운영 형태의 클라이언트/서버 설정 샘플**입니다. 배선 원리는
[`../FILE_TRANSFER.md`](../FILE_TRANSFER.md)를 참고하세요.

| 파일 | 용도 |
|---|---|
| `client.config.yaml` | NAT 뒤 클라이언트(site-a). 수신기를 터널로 노출 + 서버로 보낼 포트포워드 |
| `server.config.yaml` | 공인 IP 서버(site-b). 클라이언트로 dial + 파일 수신 |

## 빠른 시작

```bash
# 1) 샘플을 복사
cp examples/client.config.yaml config.yaml   # 클라이언트 머신
cp examples/server.config.yaml config.yaml   # 서버 머신

# 2) CHANGE-ME 값을 채움 (아래 표 참고)

# 3) 실행
./reversproxy-client --config config.yaml     # 클라이언트
./reversproxy-server --config config.yaml     # 서버

# 4) 파일 보내기 (CLI)
reversproxy-client send -to http://127.0.0.1:8090 -token <ft-token> ./report.pdf
```

## 반드시 바꿔야 하는 값

| 키 | 설명 |
|---|---|
| `auth_token` (양쪽 동일) | 터널 사전 인증 토큰. 비우거나 `changeme` 이면 `insecure:false` 에서 시작 거부 |
| `file_transfer.token` (양쪽 동일) | 파일 업로드 공유 비밀 |
| `server.config.yaml` → `clients[].address` | 클라이언트 `listen_addr` 에 닿는 실제 host:port |
| `admin_token` | 관리 API Bearer 토큰 |
| `cert_path` / `key_path` | 각 측 TLS 인증서/키 |

## 포트 배선 요약

```
[클라이언트 site-a]                                   [서버 site-b]
 수신기 127.0.0.1:8089  ◄── tunnel(공개 18089) ◄──  send_endpoint http://127.0.0.1:18089
 send_endpoint                                       수신기 127.0.0.1:8089
   http://127.0.0.1:8090 ── port-forward ──────────►  (서버가 127.0.0.1:8089 로 dial)
```

- 클라이언트의 `tunnels[].local_port` 는 `file_transfer.receive_addr` 포트(8089)와 같아야 합니다.
- 클라이언트의 `port_forwards[].local_port`(8090)가 클라이언트의 `send_endpoint` 대상이 됩니다.
- 서버의 `send_endpoint`(18089)는 클라이언트가 터널로 노출한 공개 포트입니다.

> 윈도우 GUI 설정 창에는 파일 전송 항목이 없습니다. 트레이의 **"설정 파일 열기"**로
> `config.yaml` 의 `file_transfer` 블록을 직접 편집하세요.
