#!/bin/bash
# ============================================================
# reversproxy-client 원파일 데몬 스크립트
# ============================================================
# 이 파일과 reversproxy-client 바이너리를 같은 폴더에 넣고 실행
#
# 사용법:
#   ./run-client.sh              # 시작 (기존 프로세스 자동 종료)
#   ./run-client.sh stop         # 종료
#   ./run-client.sh status       # 상태
#   ./run-client.sh logs         # 로그
#   ./run-client.sh restart      # 재시작
# ============================================================

# ── 설정 (필요시 수정) ──────────────────────────────
SERVER_IP="192.168.0.5"
RDP_LOCAL_PORT=13389
# ─────────────────────────────────────────────────────

DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$DIR/reversproxy-client"
PID="$DIR/.pid"
LOG="$DIR/client.log"
CFG="$DIR/config.yaml"

start() {
    stop 2>/dev/null

    if [ ! -f "$BIN" ]; then
        echo "ERROR: $BIN 없음"
        exit 1
    fi
    chmod +x "$BIN"

    cat > "$CFG" << 'YAML'
listen_addr: ":8443"
auth_token: "changeme"
name: "wsl-client"
socks_addr: ":1080"
http_proxy_addr: ":8080"
YAML

    nohup "$BIN" --config "$CFG" >> "$LOG" 2>&1 &
    echo $! > "$PID"
    sleep 2

    if ! kill -0 "$(cat "$PID")" 2>/dev/null; then
        echo "ERROR: 시작 실패"
        tail -5 "$LOG"
        exit 1
    fi

    # RDP 포워딩: localhost:13389 → 서버 RDP
    if command -v socat &>/dev/null; then
        nohup socat TCP-LISTEN:$RDP_LOCAL_PORT,fork,reuseaddr,bind=0.0.0.0 SOCKS4A:127.0.0.1:$SERVER_IP:3389,socksport=1080 >> "$LOG" 2>&1 &
        RDP_MSG="RDP:     mstsc → localhost:$RDP_LOCAL_PORT"
    else
        RDP_MSG="RDP:     socat 없음 (sudo apt install socat)"
    fi

    echo "========================================="
    echo " reversproxy STARTED (PID: $(cat "$PID"))"
    echo "========================================="
    echo " SOCKS5:  socks5h://127.0.0.1:1080"
    echo " HTTP:    http://127.0.0.1:8080"
    echo " $RDP_MSG"
    echo " Log:     $LOG"
    echo ""
    echo " Claude:  claude"
    echo "========================================="
}

stop() {
    if [ -f "$PID" ]; then
        kill "$(cat "$PID")" 2>/dev/null
        rm -f "$PID"
    fi
    pkill -9 -f "reversproxy-client" 2>/dev/null
    pkill -9 -f "socat.*$RDP_LOCAL_PORT" 2>/dev/null
    echo "Stopped"
}

status() {
    if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
        echo "Running (PID: $(cat "$PID"))"
    else
        echo "Not running"
    fi
}

logs() {
    if [ -f "$LOG" ]; then
        tail -f "$LOG"
    else
        echo "No log"
    fi
}

case "${1:-start}" in
    start)   start ;;
    stop)    stop ;;
    status)  status ;;
    logs)    logs ;;
    restart) stop; sleep 1; start ;;
    *)       echo "Usage: $0 {start|stop|status|logs|restart}" ;;
esac
