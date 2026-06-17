//go:build windows

// Package main implements a Windows system-tray GUI wrapper for the reversproxy client.
// It provides the same tunnel/SOCKS/HTTP-proxy functionality as cmd/client/main.go,
// exposed through a taskbar tray icon instead of a CLI.
package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EcoKG/reversproxy/internal/client"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/filetransfer"
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

// currentState tracks live connection status for tray updates.
var currentState atomic.Int32

// configPath and logPath are resolved in onReady and read by the management
// console (console.go) and settings dialog (settings.go).
var (
	configPath string
	logPath    string
)

// cancelFn holds the current tunnel context canceller; reconnectFn performs a
// disconnect+reconnect (wired in onReady, invoked by the console). Protected by mu.
var (
	mu          sync.Mutex
	cancelFn    context.CancelFunc
	reconnectFn func()
)

// reconnectTunnel disconnects and re-establishes the tunnel. Invoked from the
// management console's "재연결" button.
func reconnectTunnel() {
	mu.Lock()
	fn := reconnectFn
	mu.Unlock()
	if fn != nil {
		go fn()
	}
}

func main() {
	// Handle CLI-style subcommands (send-file / register-menu / unregister-menu)
	// before launching the tray; each exits the process when matched.
	dispatchWinSubcommands()
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
	mConfig := systray.AddMenuItem("설정 파일 열기", "config.yaml 편집")
	mDrop := systray.AddMenuItem("수신함 열기", "받은 파일 폴더 열기")
	systray.AddSeparator()
	mReg := systray.AddMenuItem("우클릭 메뉴 등록", "탐색기 우클릭에 '파일 전송' 추가")
	mUnreg := systray.AddMenuItem("우클릭 메뉴 해제", "탐색기 우클릭 메뉴 제거")
	systray.AddSeparator()
	mConsole := systray.AddMenuItem("관리 콘솔 열기", "상태/로그 보기")
	mSettings := systray.AddMenuItem("설정", "GUI로 설정 편집")
	mReconnect := systray.AddMenuItem("재연결", "연결 끊고 다시 연결")
	mAutostart := systray.AddMenuItemCheckbox("로그온 시 자동 실행", "Windows 로그온 시 자동 시작", isAutoStartEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("종료", "프로그램 종료")

	// Determine exe directory; fall back to "." if os.Executable fails.
	exePath, exeErr := os.Executable()
	exeDir := "."
	if exeErr == nil {
		exeDir = filepath.Dir(exePath)
	}
	configPath = filepath.Join(exeDir, "config.yaml")

	// Open log file next to the executable.
	logPath = filepath.Join(exeDir, "winclient.log")
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if logErr != nil {
		showError("Reversproxy", "로그 파일을 열 수 없습니다:\n"+logErr.Error())
		return
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, nil))

	// Root context; replaced per-connection by toggle.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	mu.Lock()
	cancelFn = rootCancel
	mu.Unlock()

	// ---- File-transfer feature: drop-folder receiver + right-click sender ----
	// Lives for the whole tray lifetime (rootCtx), independent of tunnel toggles.
	notify := func(msg string, ok bool) {
		if ok {
			systray.SetTooltip("Reversproxy — " + msg)
		} else {
			showError("Reversproxy 파일전송", msg)
		}
	}
	var dropDirAbs string
	if ftCfg, ferr := config.LoadClientConfig(configPath); ferr == nil && ftCfg.FileTransfer.Enabled {
		ft := ftCfg.FileTransfer
		dropDirAbs = ft.DropDir
		if dropDirAbs != "" && !filepath.IsAbs(dropDirAbs) {
			dropDirAbs = filepath.Join(exeDir, dropDirAbs)
		}
		if recv, rerr := filetransfer.NewReceiver(dropDirAbs, ft.Token, ft.MaxFileSize, log); rerr != nil {
			log.Error("file transfer: receiver init failed", "err", rerr)
		} else {
			recv.OnComplete(func(p string, _ int64) { notify("파일 도착: "+filepath.Base(p), true) })
			if addr, serr := filetransfer.StartReceiver(rootCtx, ft.ReceiveAddr, recv); serr != nil {
				log.Error("file transfer: receiver start failed", "err", serr)
			} else {
				dropDirAbs = recv.Dir()
				log.Info("file transfer receiver listening", "addr", addr, "drop_dir", dropDirAbs)
			}
		}
		if ft.ControlAddr != "" {
			if cerr := startControlServer(rootCtx, ft.ControlAddr, ft.SendEndpoint, ft.Token, log, notify); cerr != nil {
				log.Error("file transfer: control server failed", "err", cerr)
			} else {
				log.Info("file transfer control server listening", "addr", ft.ControlAddr)
			}
		}
	}

	// Wire reconnect (used by the management console) and seed the status
	// snapshot for the console/settings views.
	reconnectFn = func() {
		mu.Lock()
		if cancelFn != nil {
			cancelFn()
		}
		mu.Unlock()
		updateStatus(mStatus, mToggle, stateDisconnected)
		time.Sleep(500 * time.Millisecond)
		if _, statErr := os.Stat(configPath); statErr != nil {
			return
		}
		rcfg, lerr := config.LoadClientConfig(configPath)
		if lerr != nil {
			return
		}
		ctx, cancel := context.WithCancel(rootCtx)
		mu.Lock()
		cancelFn = cancel
		mu.Unlock()
		updateStatus(mStatus, mToggle, stateConnecting)
		go runTunnelLoop(ctx, rcfg, mStatus, mToggle)
	}
	if scfg, serr := config.LoadClientConfig(configPath); serr == nil {
		status.setConfig(scfg, configPath, logPath)
	} else {
		status.setConfig(nil, configPath, logPath)
	}

	// Auto-connect on startup: check file existence first, then load.
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		const tmpl = "listen_addr: \":8443\"\nauth_token: \"changeme\"\nname: \"my-pc\"\nlog_level: \"info\"\ninsecure: false\ntunnels: []\n"
		if wErr := os.WriteFile(configPath, []byte(tmpl), 0644); wErr != nil {
			log.Error("config.yaml 생성 실패", "err", wErr)
			showError("Reversproxy", "config.yaml 생성 실패:\n"+wErr.Error())
		} else {
			log.Info("config.yaml 생성됨", "path", configPath)
			openConfigFile(configPath)
		}
		updateStatus(mStatus, mToggle, stateDisconnected)
	} else {
		cfg, err := config.LoadClientConfig(configPath)
		if err == nil {
			ctx, cancel := context.WithCancel(rootCtx)
			mu.Lock()
			cancelFn = cancel
			mu.Unlock()
			// Publish "connecting" before spawning so the menu loop's
			// check-then-act on currentState can't mistake an in-progress
			// startup for "disconnected" (see toggle branch below).
			updateStatus(mStatus, mToggle, stateConnecting)
			go runTunnelLoop(ctx, cfg, mStatus, mToggle)
		} else {
			log.Error("config.yaml 로드 실패", "err", err)
			showError("Reversproxy", "설정 파일 오류:\n"+err.Error())
			updateStatus(mStatus, mToggle, stateDisconnected)
		}
	}

	// Menu event loop.
	for {
		select {
		case <-mToggle.ClickedCh:
			state := connStateVal(currentState.Load())
			if state == stateDisconnected {
				// Start connecting.
				if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
					showError("Reversproxy", "config.yaml 파일이 없습니다.\n트레이 메뉴 → 설정 파일 열기를 눌러 설정 후 다시 시도하세요.")
					updateStatus(mStatus, mToggle, stateDisconnected)
					continue
				}
				reloadedCfg, loadErr := config.LoadClientConfig(configPath)
				if loadErr != nil {
					showError("Reversproxy", "설정 파일 오류:\n"+loadErr.Error())
					updateStatus(mStatus, mToggle, stateDisconnected)
					continue
				}
				ctx, cancel := context.WithCancel(rootCtx)
				mu.Lock()
				cancelFn = cancel
				mu.Unlock()
				// Publish "connecting" synchronously on the menu goroutine BEFORE
				// spawning the loop. runTunnelLoop only sets it after cert+listen,
				// so without this a rapid second click would still read
				// stateDisconnected, re-enter this branch, overwrite cancelFn
				// (losing the first run's cancel) and start a duplicate run that
				// orphans the first TLS listener until process exit.
				updateStatus(mStatus, mToggle, stateConnecting)
				go runTunnelLoop(ctx, reloadedCfg, mStatus, mToggle)
			} else {
				// Disconnect.
				mu.Lock()
				if cancelFn != nil {
					cancelFn()
				}
				mu.Unlock()
				updateStatus(mStatus, mToggle, stateDisconnected)
			}

		case <-mConfig.ClickedCh:
			openConfigFile(configPath)

		case <-mDrop.ClickedCh:
			if dropDirAbs != "" {
				openFolder(dropDirAbs)
			} else {
				showError("Reversproxy", "수신함이 설정되지 않았습니다.\nconfig.yaml에서 file_transfer.enabled 와 drop_dir 를 설정하세요.")
			}

		case <-mReg.ClickedCh:
			registerContextMenu()

		case <-mUnreg.ClickedCh:
			unregisterContextMenu()

		case <-mConsole.ClickedCh:
			openConsole()

		case <-mSettings.ClickedCh:
			openSettingsDialog()

		case <-mReconnect.ClickedCh:
			reconnectTunnel()

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
			mu.Lock()
			if cancelFn != nil {
				cancelFn()
			}
			mu.Unlock()
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

// runTunnelLoop starts the TLS listener and the accept loop.
// It updates the tray status as connection state changes.
// Runs until ctx is cancelled.
func runTunnelLoop(
	ctx context.Context,
	cfg *config.ClientConfig,
	mStatus *systray.MenuItem,
	mToggle *systray.MenuItem,
) {
	log := logger.NewWithLevel("winclient", cfg.LogLevel)

	rcCfg := buildClientConfig(cfg)

	cert, err := control.LoadOrGenerateCert(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Error("failed to load or generate TLS certificate", "err", err)
		updateStatus(mStatus, mToggle, stateDisconnected)
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
		updateStatus(mStatus, mToggle, stateDisconnected)
		return
	}
	defer ln.Close()

	// Close listener when context is cancelled so Accept() returns.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	// Server pool — multi-server support, mirroring cmd/client. Each connected
	// proxy server is an independent session with its own writer and SOCKS mux.
	// Client-originated SOCKS5 / HTTP CONNECT / port-forward requests pick a
	// session round-robin; external traffic flows through its own session.
	pool := client.NewServerPool()

	if cfg.SOCKSAddr != "" {
		if err := socks.StartClientSOCKSProxy(ctx, cfg.SOCKSAddr, pool, log, cfg.SOCKSUser, cfg.SOCKSPass); err != nil {
			log.Error("failed to start SOCKS5 proxy", "addr", cfg.SOCKSAddr, "err", err)
		}
	}
	if cfg.HTTPProxyAddr != "" {
		if err := socks.StartHTTPConnectProxy(ctx, cfg.HTTPProxyAddr, pool, log); err != nil {
			log.Error("failed to start HTTP CONNECT proxy", "addr", cfg.HTTPProxyAddr, "err", err)
		}
	}
	for _, pf := range cfg.PortForwards {
		if err := socks.StartPortForward(ctx, pf.LocalPort, pf.RemoteHost, pf.RemotePort, pf.Bind, pool, log); err != nil {
			log.Error("failed to start port forward", "localPort", pf.LocalPort, "err", err)
		}
	}

	updateStatus(mStatus, mToggle, stateConnecting)

	// Accept loop — each inbound server connection is handled in its own
	// goroutine so multiple servers can connect concurrently.
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				log.Error("accept error", "err", err)
			}
			updateStatus(mStatus, mToggle, stateDisconnected)
			return
		}
		go handleServerSession(ctx, conn, cfg, rcCfg, pool, log, mStatus, mToggle)
	}
}

// handleServerSession wraps one inbound server connection in a ServerSession,
// adds it to the pool for the duration of the handshake + message loop, and
// removes it on exit. Tray status follows the pool's active-session count:
// the first connection flips the tray to "connected"; when the last session
// drops (and we are still running) the tray returns to "waiting for reconnect".
func handleServerSession(
	ctx context.Context,
	conn net.Conn,
	cfg *config.ClientConfig,
	rcCfg *reconnect.ClientConfig,
	pool *client.ServerPool,
	log *slog.Logger,
	mStatus *systray.MenuItem,
	mToggle *systray.MenuItem,
) {
	session := &client.ServerSession{
		ID:          conn.RemoteAddr().String(),
		Conn:        conn,
		Writer:      client.NewConnWriter(conn),
		Mux:         tunnel.NewSOCKSMux(),
		Addr:        conn.RemoteAddr().String(),
		ConnectedAt: time.Now(),
	}

	pool.Add(session)
	updateStatus(mStatus, mToggle, stateConnected)
	log.Info("server connected", "remote", session.Addr, "active_servers", pool.Len())

	client.HandleServerConn(ctx, session, cfg.AuthToken, cfg.Name, rcCfg, log)

	pool.Remove(session)
	remaining := pool.Len()
	log.Warn("server connection lost", "remote", session.Addr, "active_servers", remaining)
	if remaining == 0 && ctx.Err() == nil {
		updateStatus(mStatus, mToggle, stateConnecting)
	}
}

// updateStatus sets the connection state and updates the tray tooltip and menu labels.
func updateStatus(mStatus *systray.MenuItem, mToggle *systray.MenuItem, state connStateVal) {
	currentState.Store(int32(state))
	status.setState(state, "")
	switch state {
	case stateDisconnected:
		mStatus.SetTitle("상태: 연결 안됨")
		mToggle.SetTitle("연결")
		systray.SetTooltip("Reversproxy — 연결 안됨")
	case stateConnecting:
		mStatus.SetTitle("상태: 대기 중 (서버 연결 기다리는 중)")
		mToggle.SetTitle("연결 해제")
		systray.SetTooltip("Reversproxy — 서버 연결 대기 중")
	case stateConnected:
		mStatus.SetTitle("상태: 연결됨")
		mToggle.SetTitle("연결 해제")
		systray.SetTooltip("Reversproxy — 연결됨")
	}
}

// openConfigFile opens configPath with the system default editor on Windows.
func openConfigFile(configPath string) {
	_ = exec.Command("cmd", "/c", "start", "", configPath).Start()
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
