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
LISTEN=":8443"
TOKEN="changeme"
NAME="$(hostname)"
LOCAL_PORT=80
SOCKS=":1080"
HTTP_PROXY=":8080"
# ─────────────────────────────────────────────────────

DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$DIR/reversproxy-client"
PID="$DIR/.pid"
LOG="$DIR/client.log"

start() {
    # 기존 프로세스 전부 종료
    stop 2>/dev/null

    if [ ! -f "$BIN" ]; then
        echo "ERROR: $BIN 없음"
        exit 1
    fi
    chmod +x "$BIN"

    nohup "$BIN" \
        --listen "$LISTEN" \
        --token "$TOKEN" \
        --name "$NAME" \
        --local-port "$LOCAL_PORT" \
        --socks-addr "$SOCKS" \
        --http-proxy-addr "$HTTP_PROXY" \
        >> "$LOG" 2>&1 &

    echo $! > "$PID"
    sleep 2

    if kill -0 "$(cat "$PID")" 2>/dev/null; then
        echo "========================================="
        echo " reversproxy-client STARTED (PID: $(cat "$PID"))"
        echo "========================================="
        echo " SOCKS5:  socks5h://127.0.0.1${SOCKS}"
        echo " HTTP:    http://127.0.0.1${HTTP_PROXY}"
        echo " Log:     $LOG"
        echo ""
        echo " Claude:  HTTPS_PROXY=http://127.0.0.1${HTTP_PROXY} claude"
        echo " 또는:    export HTTPS_PROXY=http://127.0.0.1${HTTP_PROXY}"
        echo "          claude"
        echo "========================================="
    else
        echo "ERROR: 시작 실패"
        tail -5 "$LOG"
        exit 1
    fi
}

stop() {
    # PID 파일로 종료
    if [ -f "$PID" ]; then
        kill "$(cat "$PID")" 2>/dev/null
        rm -f "$PID"
    fi
    # 혹시 남은 프로세스도 정리
    pkill -9 -f "reversproxy-client" 2>/dev/null
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
