#!/bin/bash
# ============================================================
# CloseProxy WSL 전체 설정 스크립트
# 192.168.1.221 (폐쇄망) WSL에서 실행
# ============================================================
# 사용법: sudo bash /mnt/c/Users/smlee/closeProxy/setup-wsl.sh
# ============================================================

set -e

PROXY="http://127.0.0.1:8080"
SERVER_SCRIPT="/mnt/c/Users/smlee/closeProxy/server.py"
LOG_FILE="/var/log/closeproxy.log"
SERVICE_SCRIPT="/usr/local/bin/closeproxy.sh"
USER_NAME=$(logname 2>/dev/null || echo "$SUDO_USER")

echo "========================================"
echo " CloseProxy WSL Setup"
echo "========================================"
echo

# ==================== 1. 프록시 환경변수 ====================
echo "[1/6] 프록시 환경변수 설정..."
cat > /etc/profile.d/proxy.sh << EOF
export HTTP_PROXY=$PROXY
export HTTPS_PROXY=$PROXY
export http_proxy=$PROXY
export https_proxy=$PROXY
export NO_PROXY=localhost,127.0.0.1
EOF
chmod +x /etc/profile.d/proxy.sh
echo "      /etc/profile.d/proxy.sh OK"

# ==================== 2. apt 프록시 ====================
echo "[2/6] apt 프록시 설정..."
cat > /etc/apt/apt.conf.d/proxy.conf << EOF
Acquire::http::Proxy "$PROXY";
Acquire::https::Proxy "$PROXY";
EOF
echo "      /etc/apt/apt.conf.d/proxy.conf OK"

# ==================== 3. wget 프록시 ====================
echo "[3/6] wget 프록시 설정..."
if ! grep -q "use_proxy=yes" /etc/wgetrc 2>/dev/null; then
    cat >> /etc/wgetrc << EOF

use_proxy=yes
http_proxy=$PROXY
https_proxy=$PROXY
EOF
fi
echo "      /etc/wgetrc OK"

# ==================== 4. git 프록시 ====================
echo "[4/6] git 프록시 설정..."
if command -v git &>/dev/null; then
    sudo -u "$USER_NAME" git config --global http.proxy "$PROXY"
    sudo -u "$USER_NAME" git config --global https.proxy "$PROXY"
    echo "      git config OK"
else
    echo "      git 미설치 - 건너뜀"
fi

# ==================== 5. 서비스 스크립트 ====================
echo "[5/6] CloseProxy 서비스 등록..."
cat > "$SERVICE_SCRIPT" << 'SCRIPT'
#!/bin/bash
case "${1:-start}" in
    start)
        if pgrep -f "python3.*server.py" > /dev/null 2>&1; then
            echo "CloseProxy already running (PID: $(pgrep -f 'python3.*server.py'))"
        else
            nohup python3 /mnt/c/Users/smlee/closeProxy/server.py > /var/log/closeproxy.log 2>&1 &
            sleep 1
            if pgrep -f "python3.*server.py" > /dev/null 2>&1; then
                echo "CloseProxy started (PID: $(pgrep -f 'python3.*server.py'))"
            else
                echo "CloseProxy failed to start. Check /var/log/closeproxy.log"
                exit 1
            fi
        fi
        ;;
    stop)
        if pkill -f "python3.*server.py" 2>/dev/null; then
            echo "CloseProxy stopped"
        else
            echo "CloseProxy not running"
        fi
        ;;
    restart)
        $0 stop
        sleep 1
        $0 start
        ;;
    status)
        if pgrep -f "python3.*server.py" > /dev/null 2>&1; then
            echo "CloseProxy running (PID: $(pgrep -f 'python3.*server.py'))"
        else
            echo "CloseProxy not running"
        fi
        ;;
    log)
        tail -f /var/log/closeproxy.log
        ;;
    *)
        echo "Usage: closeproxy {start|stop|restart|status|log}"
        ;;
esac
SCRIPT
chmod +x "$SERVICE_SCRIPT"
echo "      $SERVICE_SCRIPT OK"

# ==================== 6. 자동 시작 (cron) ====================
echo "[6/6] 부팅 시 자동 시작 등록..."
CRON_LINE="@reboot $SERVICE_SCRIPT start"
(crontab -u "$USER_NAME" -l 2>/dev/null | grep -v "closeproxy.sh"; echo "$CRON_LINE") | crontab -u "$USER_NAME" -
echo "      crontab OK"

# ==================== 서비스 시작 ====================
echo
echo "CloseProxy 서비스 시작 중..."
$SERVICE_SCRIPT start

# 현재 셸에 프록시 적용
source /etc/profile.d/proxy.sh

echo
echo "========================================"
echo " Setup Complete!"
echo "========================================"
echo
echo " 프록시:    $PROXY"
echo " 서비스:    closeproxy.sh {start|stop|restart|status|log}"
echo " 로그:      tail -f $LOG_FILE"
echo
echo " 제어 명령:"
echo "   closeproxy.sh start    # 시작"
echo "   closeproxy.sh stop     # 중지"
echo "   closeproxy.sh restart  # 재시작"
echo "   closeproxy.sh status   # 상태 확인"
echo "   closeproxy.sh log      # 로그 보기"
echo
echo " 새 터미널에서 프록시 적용 확인:"
echo "   echo \$HTTPS_PROXY"
echo
echo "========================================"
