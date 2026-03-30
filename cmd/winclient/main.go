//go:build windows

// Package main implements a Windows system-tray GUI wrapper for the reversproxy client.
// It provides the same tunnel/SOCKS/HTTP-proxy functionality as cmd/client/main.go,
// exposed through a taskbar tray icon instead of a CLI.
package main

import (
	"context"
	"crypto/tls"
	_ "embed"
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

//go:embed assets/icon.png
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

// cancelFn holds the current tunnel context canceller. Protected by mu.
var (
	mu       sync.Mutex
	cancelFn context.CancelFunc
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
	mConfig := systray.AddMenuItem("설정 파일 열기", "config.yaml 편집")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("종료", "프로그램 종료")

	// Determine config file path: same directory as the executable.
	exePath, _ := os.Executable()
	configPath := filepath.Join(filepath.Dir(exePath), "config.yaml")

	// Root context; replaced per-connection by toggle.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	mu.Lock()
	cancelFn = rootCancel
	mu.Unlock()

	// Auto-connect on startup if config.yaml is loadable.
	cfg, err := config.LoadClientConfig(configPath)
	if err == nil {
		ctx, cancel := context.WithCancel(rootCtx)
		mu.Lock()
		cancelFn = cancel
		mu.Unlock()
		go runTunnelLoop(ctx, cfg, mStatus, mToggle)
	} else {
		updateStatus(mStatus, mToggle, stateDisconnected)
	}

	// Menu event loop.
	for {
		select {
		case <-mToggle.ClickedCh:
			state := connStateVal(currentState.Load())
			if state == stateDisconnected {
				// Start connecting.
				reloadedCfg, loadErr := config.LoadClientConfig(configPath)
				if loadErr != nil {
					updateStatus(mStatus, mToggle, stateDisconnected)
					continue
				}
				ctx, cancel := context.WithCancel(rootCtx)
				mu.Lock()
				cancelFn = cancel
				mu.Unlock()
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
		updateStatus(mStatus, mToggle, stateDisconnected)
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
		if err := socks.StartHTTPConnectProxy(ctx, cfg.HTTPProxyAddr, sharedWriter, clientMux, log); err != nil {
			log.Error("failed to start HTTP CONNECT proxy", "addr", cfg.HTTPProxyAddr, "err", err)
		}
	}
	for _, pf := range cfg.PortForwards {
		if err := socks.StartPortForward(ctx, pf.LocalPort, pf.RemoteHost, pf.RemotePort, pf.Bind, sharedWriter, clientMux, log); err != nil {
			log.Error("failed to start port forward", "localPort", pf.LocalPort, "err", err)
		}
	}

	updateStatus(mStatus, mToggle, stateConnecting)

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

		log.Info("server connected", "remote", conn.RemoteAddr())
		updateStatus(mStatus, mToggle, stateConnected)

		sharedWriter.SwapConn(conn)
		clientMux.CloseAll()

		client.HandleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, sharedWriter, clientMux, log)

		sharedWriter.ClearConn()
		updateStatus(mStatus, mToggle, stateConnecting)
		log.Warn("server connection lost, waiting for reconnect")
	}
}

// updateStatus sets the connection state and updates the tray tooltip and menu labels.
func updateStatus(mStatus *systray.MenuItem, mToggle *systray.MenuItem, state connStateVal) {
	currentState.Store(int32(state))
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
