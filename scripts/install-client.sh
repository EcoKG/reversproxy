#!/bin/bash
set -e

REPO="EcoKG/reversproxy"
INSTALL_DIR="$HOME/reversproxy"
BIN="$INSTALL_DIR/reversproxy-client"

echo "==> reversproxy-client installer"

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *) echo "Unsupported: $ARCH"; exit 1 ;;
esac

# Download
URL="https://github.com/$REPO/releases/latest/download/reversproxy-client-linux-$ARCH"
mkdir -p "$INSTALL_DIR"
echo "==> Downloading $URL"
curl -fsSL -o "$BIN" "$URL"
chmod +x "$BIN"

# Create run script
cat > "$INSTALL_DIR/rproxy" << 'SCRIPT'
#!/bin/bash
DIR="$HOME/reversproxy"
BIN="$DIR/reversproxy-client"
PID="$DIR/.pid"
LOG="$DIR/client.log"

# ── 설정 ──────────────────────────
LISTEN=":8443"
TOKEN="changeme"
NAME="$(hostname)"
LOCAL_PORT=80
SOCKS=":1080"
HTTP_PROXY=":8080"
# ──────────────────────────────────

case "${1:-start}" in
  start)
    pkill -9 -f reversproxy-client 2>/dev/null; rm -f "$PID"
    nohup "$BIN" --listen "$LISTEN" --token "$TOKEN" --name "$NAME" \
      --local-port "$LOCAL_PORT" --socks-addr "$SOCKS" \
      --http-proxy-addr "$HTTP_PROXY" >> "$LOG" 2>&1 &
    echo $! > "$PID"; sleep 2
    if kill -0 "$(cat "$PID")" 2>/dev/null; then
      echo "reversproxy started (PID: $(cat "$PID"))"
      echo "  SOCKS5: socks5h://127.0.0.1${SOCKS}"
      echo "  HTTP:   http://127.0.0.1${HTTP_PROXY}"
      echo "  Claude: HTTPS_PROXY=http://127.0.0.1${HTTP_PROXY} claude"
    else
      echo "Failed"; tail -5 "$LOG"; exit 1
    fi ;;
  stop)
    pkill -9 -f reversproxy-client 2>/dev/null; rm -f "$PID"
    echo "Stopped" ;;
  status)
    [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null && echo "Running ($(cat "$PID"))" || echo "Not running" ;;
  logs)
    tail -f "$LOG" ;;
  restart)
    "$0" stop; sleep 1; "$0" start ;;
  *)
    echo "Usage: rproxy {start|stop|status|logs|restart}" ;;
esac
SCRIPT
chmod +x "$INSTALL_DIR/rproxy"

# Add to PATH
if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
  for RC in "$HOME/.bashrc" "$HOME/.zshrc"; do
    [ -f "$RC" ] && ! grep -q "reversproxy" "$RC" && echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$RC"
  done
  export PATH="$INSTALL_DIR:$PATH"
fi

echo ""
echo "==> Done!"
echo ""
echo "  rproxy              # 시작"
echo "  rproxy stop         # 종료"
echo "  rproxy restart      # 재시작"
echo "  rproxy status       # 상태"
echo "  rproxy logs         # 로그"
echo ""
echo "  source ~/.bashrc    # PATH 적용 (최초 1회)"
echo ""
