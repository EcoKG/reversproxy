//go:build windows

// Package main implements a Windows system-tray GUI wrapper for the reversproxy client.
// It provides the same tunnel/SOCKS/HTTP-proxy functionality as cmd/client/main.go,
// exposed through a taskbar tray icon, a native management console (lxn/walk), and a
// settings dialog instead of a CLI.
package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EcoKG/reversproxy/internal/client"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/reconnect"
	"github.com/EcoKG/reversproxy/internal/socks"
	"github.com/EcoKG/reversproxy/internal/tunnel"
	"github.com/getlantern/systray"
)

//go:embed assets/icon.ico
var iconData []byte

// connStateVal represents the current tunnel connection state.
type connStateVal int32

const (
	stateDisconnected connStateVal = iota
	stateConnecting
	stateConnected
)

// currentState tracks live connection status for the tray toggle logic.
var currentState atomic.Int32

// Process-wide control state, set up in onReady and used by the tray loop, the
// management console, and the settings dialog.
var (
	mu          sync.Mutex
	cancelFn    context.CancelFunc // cancels the current tunnel context
	rootCtx     context.Context    // parent context for all tunnel runs
	configPath  string
	logPath     string
	appLog      *slog.Logger
	mStatusItem *systray.MenuItem
	mToggleItem *systray.MenuItem
)

func main() {
	systray.Run(onReady, onExit)
}

// onReady is called by systray on the main goroutine once the tray is ready.
func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("Reversproxy")
	systray.SetTooltip("Reversproxy Client — 연결 안됨")

	mStatus := systray.AddMenuItem("상태: 연결 안됨", "현재 연결 상태")
	mStatus.Disable()
	systray.AddSeparator()
	mToggle := systray.AddMenuItem("연결", "서버에 연결/해제")
	mReconnect := systray.AddMenuItem("재연결", "연결을 끊고 다시 연결")
	systray.AddSeparator()
	mConsole := systray.AddMenuItem("관리 콘솔 열기", "상태를 보는 관리 콘솔 창")
	mSettings := systray.AddMenuItem("설정", "GUI로 설정 편집")
	mConfigFile := systray.AddMenuItem("설정 파일 열기 (고급)", "config.yaml 직접 편집")
	mLogs := systray.AddMenuItem("로그 파일 열기", "winclient.log 열기")
	mAutostart := systray.AddMenuItemCheckbox("시작 시 자동 실행", "Windows 로그온 시 자동 시작", isAutoStartEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("종료", "프로그램 종료")

	mStatusItem = mStatus
	mToggleItem = mToggle

	// Determine exe directory; fall back to "." if os.Executable fails.
	exePath, exeErr := os.Executable()
	exeDir := "."
	if exeErr == nil {
		exeDir = filepath.Dir(exePath)
	}
	configPath = filepath.Join(exeDir, "config.yaml")
	logPath = filepath.Join(exeDir, "winclient.log")

	// Open log file next to the executable.
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if logErr != nil {
		showError("Reversproxy", "로그 파일을 열 수 없습니다:\n"+logErr.Error())
		return
	}
	defer logFile.Close()
	appLog = slog.New(slog.NewTextHandler(logFile, nil))

	// Root context; child contexts are created per connection by startTunnel.
	rc, rootCancel := context.WithCancel(context.Background())
	rootCtx = rc
	defer rootCancel()

	mu.Lock()
	cancelFn = rootCancel
	mu.Unlock()

	status.setConfig(nil, configPath, logPath)

	// Auto-connect on startup: create a default config if missing, else connect.
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		const tmpl = "listen_addr: \":8443\"\nauth_token: \"changeme\"\nname: \"my-pc\"\nlog_level: \"info\"\ninsecure: false\ntunnels: []\n"
		if wErr := os.WriteFile(configPath, []byte(tmpl), 0644); wErr != nil {
			appLog.Error("config.yaml 생성 실패", "err", wErr)
			showError("Reversproxy", "config.yaml 생성 실패:\n"+wErr.Error())
		} else {
			appLog.Info("config.yaml 생성됨", "path", configPath)
			openConfigFile(configPath)
		}
		updateStatus(stateDisconnected)
	} else {
		startTunnel()
	}

	// Menu event loop.
	for {
		select {
		case <-mToggle.ClickedCh:
			if connStateVal(currentState.Load()) == stateDisconnected {
				startTunnel()
			} else {
				stopTunnel()
			}

		case <-mReconnect.ClickedCh:
			reconnectTunnel()

		case <-mConsole.ClickedCh:
			openConsole()

		case <-mSettings.ClickedCh:
			openSettingsDialog()

		case <-mConfigFile.ClickedCh:
			openConfigFile(configPath)

		case <-mLogs.ClickedCh:
			openConfigFile(logPath)

		case <-mAutostart.ClickedCh:
			enable := !mAutostart.Checked()
			if err := setAutoStart(enable); err != nil {
				showError("Reversproxy", "자동 실행 설정 실패:\n"+err.Error())
			} else if enable {
				mAutostart.Check()
			} else {
				mAutostart.Uncheck()
			}

		case <-mQuit.ClickedCh:
			stopTunnel()
			// Brief drain so goroutines see cancellation.
			time.Sleep(500 * time.Millisecond)
			systray.Quit()
			return
		}
	}
}

// onExit is called by systray after Quit(). Context cancellation already
// handles goroutine cleanup in onReady.
func onExit() {}

// startTunnel loads the config and starts the tunnel loop in a new context.
// It is a no-op-with-error-dialog when the config is missing or invalid.
func startTunnel() {
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		showError("Reversproxy", "config.yaml 파일이 없습니다.\n트레이 메뉴 → 설정을 눌러 설정 후 다시 시도하세요.")
		updateStatus(stateDisconnected)
		return
	}
	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		showError("Reversproxy", "설정 파일 오류:\n"+err.Error())
		updateStatus(stateDisconnected)
		return
	}

	ctx, cancel := context.WithCancel(rootCtx)
	mu.Lock()
	cancelFn = cancel
	mu.Unlock()

	status.setConfig(cfg, configPath, logPath)
	go runTunnelLoop(ctx, cfg)
}

// stopTunnel cancels the current tunnel context and marks the state disconnected.
func stopTunnel() {
	mu.Lock()
	if cancelFn != nil {
		cancelFn()
	}
	mu.Unlock()
	updateStatus(stateDisconnected)
}

// reconnectTunnel tears down the current connection and starts a fresh one.
// A short delay lets the previous TLS listener release its port before rebinding.
func reconnectTunnel() {
	stopTunnel()
	go func() {
		time.Sleep(600 * time.Millisecond)
		startTunnel()
	}()
}

// runTunnelLoop starts the TLS listener and the accept loop, updating the tray
// and live status as the connection state changes. Runs until ctx is cancelled.
func runTunnelLoop(ctx context.Context, cfg *config.ClientConfig) {
	log := logger.NewWithLevel("winclient", cfg.LogLevel)

	rcCfg := buildClientConfig(cfg)

	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		updateStatus(stateDisconnected)
		return
	}

	tlsCfg := control.NewServerTLSConfig(cert)
	if cfg.Insecure {
		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		}
	}

	ln, err := tls.Listen("tcp", cfg.ListenAddr, tlsCfg)
	if err != nil {
		log.Error("failed to start TLS listener", "addr", cfg.ListenAddr, "err", err)
		showError("Reversproxy 오류", "리스너 시작 실패 ("+cfg.ListenAddr+"):\n"+err.Error())
		updateStatus(stateDisconnected)
		return
	}
	defer ln.Close()

	// Close listener when context is cancelled so Accept() returns.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	sharedWriter := &client.ConnWriter{}
	clientMux := tunnel.NewSOCKSMux()

	if cfg.SOCKSAddr != "" {
		if err := socks.StartClientSOCKSProxy(ctx, cfg.SOCKSAddr, sharedWriter, clientMux, log, cfg.SOCKSUser, cfg.SOCKSPass); err != nil {
			log.Error("failed to start SOCKS5 proxy", "addr", cfg.SOCKSAddr, "err", err)
		}
	}
	if cfg.HTTPProxyAddr != "" {
		if err := socks.StartHTTPConnectProxy(ctx, cfg.HTTPProxyAddr, sharedWriter, clientMux, log, cfg.HTTPProxyUser, cfg.HTTPProxyPass); err != nil {
			log.Error("failed to start HTTP CONNECT proxy", "addr", cfg.HTTPProxyAddr, "err", err)
		}
	}
	for _, pf := range cfg.PortForwards {
		if err := socks.StartPortForward(ctx, pf.LocalPort, pf.RemoteHost, pf.RemotePort, pf.Bind, sharedWriter, clientMux, log); err != nil {
			log.Error("failed to start port forward", "localPort", pf.LocalPort, "err", err)
		}
	}

	updateStatus(stateConnecting)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				log.Error("accept error", "err", err)
			}
			updateStatus(stateDisconnected)
			return
		}

		log.Info("server connected", "remote", conn.RemoteAddr())
		status.setConnected(conn.RemoteAddr().String(), time.Now())
		updateStatus(stateConnected)

		sharedWriter.SwapConn(conn)
		clientMux.CloseAll()

		client.HandleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, sharedWriter, clientMux, log)

		sharedWriter.ClearConn()
		updateStatus(stateConnecting)
		log.Warn("server connection lost, waiting for reconnect")
	}
}

// updateStatus sets the connection state and updates the tray tooltip, menu
// labels, and the shared live status.
func updateStatus(state connStateVal) {
	currentState.Store(int32(state))
	status.setState(state, "")

	switch state {
	case stateDisconnected:
		setMenu("상태: 연결 안됨", "연결", "Reversproxy — 연결 안됨")
	case stateConnecting:
		setMenu("상태: 대기 중 (서버 연결 기다리는 중)", "연결 해제", "Reversproxy — 서버 연결 대기 중")
	case stateConnected:
		setMenu("상태: 연결됨", "연결 해제", "Reversproxy — 연결됨")
	}

	// Refresh the console if it is open.
	refreshConsole()
}

// setMenu updates the tray status/toggle labels and tooltip, tolerating a
// not-yet-initialised menu (defensive).
func setMenu(statusTitle, toggleTitle, tooltip string) {
	if mStatusItem != nil {
		mStatusItem.SetTitle(statusTitle)
	}
	if mToggleItem != nil {
		mToggleItem.SetTitle(toggleTitle)
	}
	systray.SetTooltip(tooltip)
}

// openConfigFile opens path with the system default editor on Windows.
func openConfigFile(path string) {
	_ = exec.Command("cmd", "/c", "start", "", path).Start()
}

// buildClientConfig converts config.ClientConfig tunnels to reconnect.ClientConfig.
func buildClientConfig(cfg *config.ClientConfig) *reconnect.ClientConfig {
	rcCfg := &reconnect.ClientConfig{}
	for _, t := range cfg.Tunnels {
		switch t.Type {
		case "tcp", "":
			rcCfg.AddTunnel(t.LocalHost, t.LocalPort, t.RequestedPort)
		case "http":
			rcCfg.AddHTTPTunnel(t.Hostname, t.LocalHost, t.LocalPort)
		case "https":
			rcCfg.AddHTTPSTunnel(t.Hostname, t.LocalHost, t.LocalPort)
		}
	}
	return rcCfg
}
