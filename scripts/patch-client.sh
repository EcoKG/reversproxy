#!/bin/bash
# ============================================================
# reversproxy client — 패치 + 데몬 실행 스크립트
# ============================================================
# 사용법:
#   1. 이 파일과 reversproxy-client 바이너리를 같은 폴더에 넣기
#   2. chmod +x patch-client.sh reversproxy-client
#   3. ./patch-client.sh
# ============================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$SCRIPT_DIR/reversproxy-client"
INSTALL_DIR="$HOME/bin"
PID_FILE="$HOME/.reversproxy-client.pid"
LOG_FILE="$HOME/reversproxy-client.log"

# ── 기본 설정 (필요시 수정) ──────────────────────────────
LISTEN_ADDR="${LISTEN_ADDR:-:8443}"
AUTH_TOKEN="${AUTH_TOKEN:-changeme}"
CLIENT_NAME="${CLIENT_NAME:-$(hostname)}"
LOCAL_PORT="${LOCAL_PORT:-80}"
SOCKS_ADDR="${SOCKS_ADDR:-:1080}"
HTTP_PROXY_ADDR="${HTTP_PROXY_ADDR:-:8080}"
# ─────────────────────────────────────────────────────────

usage() {
    echo "Usage: $0 {install|start|stop|restart|status|logs|uninstall}"
    echo ""
    echo "Commands:"
    echo "  install   - 바이너리 설치 + 환경변수 설정"
    echo "  start     - 데몬으로 실행"
    echo "  stop      - 데몬 종료"
    echo "  restart   - 재시작"
    echo "  status    - 실행 상태 확인"
    echo "  logs      - 실시간 로그 보기"
    echo "  uninstall - 제거"
    echo ""
    echo "Environment variables:"
    echo "  LISTEN_ADDR     (default: :8443)"
    echo "  AUTH_TOKEN      (default: changeme)"
    echo "  CLIENT_NAME     (default: hostname)"
    echo "  LOCAL_PORT      (default: 80)"
    echo "  SOCKS_ADDR      (default: :1080)"
    echo "  HTTP_PROXY_ADDR (default: :8080)"
}

do_install() {
    echo "==> Installing reversproxy-client"

    # Check binary exists
    if [ ! -f "$BINARY" ]; then
        echo "ERROR: $BINARY not found. Place the binary next to this script."
        exit 1
    fi

    # Install binary
    mkdir -p "$INSTALL_DIR"
    cp "$BINARY" "$INSTALL_DIR/reversproxy-client"
    chmod +x "$INSTALL_DIR/reversproxy-client"
    echo "    Binary: $INSTALL_DIR/reversproxy-client"

    # Add proxy env to .bashrc
    if ! grep -q "# reversproxy-client proxy" "$HOME/.bashrc" 2>/dev/null; then
        cat >> "$HOME/.bashrc" << 'BASHRC'

# reversproxy-client proxy
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY
export HTTPS_PROXY=http://127.0.0.1:8080
export HTTP_PROXY=http://127.0.0.1:8080
export ALL_PROXY=socks5h://127.0.0.1:1080
export NO_PROXY=localhost,127.0.0.1
BASHRC
        echo "    Proxy env added to ~/.bashrc"
    else
        echo "    Proxy env already in ~/.bashrc (skipped)"
    fi

    # Add PATH if needed
    if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
        if ! grep -q "export PATH.*$INSTALL_DIR" "$HOME/.bashrc" 2>/dev/null; then
            echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$HOME/.bashrc"
            echo "    PATH updated in ~/.bashrc"
        fi
    fi

    echo ""
    echo "==> Installation complete!"
    echo "    Run: source ~/.bashrc"
    echo "    Then: $0 start"
}

do_start() {
    # Stop if already running
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "Already running (PID: $(cat "$PID_FILE"))"
        return
    fi

    echo "==> Starting reversproxy-client (daemon)"

    nohup "$INSTALL_DIR/reversproxy-client" \
        --listen "$LISTEN_ADDR" \
        --token "$AUTH_TOKEN" \
        --name "$CLIENT_NAME" \
        --local-port "$LOCAL_PORT" \
        --socks-addr "$SOCKS_ADDR" \
        --http-proxy-addr "$HTTP_PROXY_ADDR" \
        > "$LOG_FILE" 2>&1 &

    echo $! > "$PID_FILE"
    sleep 1

    if kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "    PID: $(cat "$PID_FILE")"
        echo "    Log: $LOG_FILE"
        echo "    SOCKS5:       socks5h://127.0.0.1${SOCKS_ADDR}"
        echo "    HTTP CONNECT: http://127.0.0.1${HTTP_PROXY_ADDR}"
        echo ""
        echo "    Claude Code:  claude"
        echo "    curl test:    curl https://httpbin.org/ip"
    else
        echo "ERROR: Failed to start. Check $LOG_FILE"
        cat "$LOG_FILE"
        exit 1
    fi
}

do_stop() {
    if [ ! -f "$PID_FILE" ]; then
        echo "Not running"
        return
    fi

    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        echo "==> Stopping (PID: $PID)"
        kill "$PID"
        sleep 1
        kill -0 "$PID" 2>/dev/null && kill -9 "$PID"
        echo "    Stopped"
    else
        echo "    Process not found (stale PID file)"
    fi
    rm -f "$PID_FILE"
}

do_status() {
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "Running (PID: $(cat "$PID_FILE"))"
        echo "SOCKS5:       socks5h://127.0.0.1${SOCKS_ADDR}"
        echo "HTTP CONNECT: http://127.0.0.1${HTTP_PROXY_ADDR}"
    else
        echo "Not running"
    fi
}

do_logs() {
    if [ -f "$LOG_FILE" ]; then
        tail -f "$LOG_FILE"
    else
        echo "No log file found"
    fi
}

do_uninstall() {
    do_stop
    rm -f "$INSTALL_DIR/reversproxy-client"
    rm -f "$PID_FILE" "$LOG_FILE"
    sed -i '/# reversproxy-client proxy/,/^$/d' "$HOME/.bashrc" 2>/dev/null
    echo "==> Uninstalled"
}

# ── Main ─────────────────────────────────────────────────
case "${1:-install}" in
    install)   do_install ;;
    start)     do_start ;;
    stop)      do_stop ;;
    restart)   do_stop; sleep 1; do_start ;;
    status)    do_status ;;
    logs)      do_logs ;;
    uninstall) do_uninstall ;;
    *)         usage ;;
esac
