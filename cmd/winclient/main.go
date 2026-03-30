//go:build windows

// Package main implements a Windows system-tray GUI wrapper for the reversproxy client.
// It provides the same tunnel/SOCKS/HTTP-proxy functionality as cmd/client/main.go,
// exposed through a taskbar tray icon instead of a CLI.
package main

// Copied from cmd/client/main.go — keep in sync

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/gob"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/logger"
	"github.com/EcoKG/reversproxy/internal/protocol"
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

	sharedWriter := &clientConnWriter{}
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

		handleServerConn(ctx, conn, cfg.AuthToken, cfg.Name, rcCfg, sharedWriter, clientMux, log)

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

// ─── Copied from cmd/client/main.go — keep in sync ───────────────────────── //

// handleServerConn manages a single connection from the proxy server.
func handleServerConn(
	ctx context.Context,
	conn net.Conn,
	authToken, name string,
	cfg *reconnect.ClientConfig,
	sharedWriter *clientConnWriter,
	clientMux *tunnel.SOCKSMux,
	log *slog.Logger,
) {
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Error("failed to set registration deadline", "err", err)
		return
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Warn("failed to read registration message from server", "err", err)
		return
	}

	if env.Type != protocol.MsgClientRegister {
		log.Warn("unexpected message type during handshake", "type", env.Type)
		return
	}

	var msg protocol.ClientRegister
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&msg); err != nil {
		log.Warn("failed to decode ClientRegister from server", "err", err)
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "malformed ClientRegister payload",
		})
		return
	}

	if msg.AuthToken != authToken {
		_ = protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
			Status: "error",
			Error:  "invalid token",
		})
		log.Warn("registration rejected: invalid token from server", "remote", conn.RemoteAddr())
		return
	}

	if err := protocol.WriteMessage(conn, protocol.MsgRegisterResp, protocol.RegisterResp{
		Status:   "ok",
		ServerID: name,
	}); err != nil {
		log.Error("failed to send RegisterResp", "err", err)
		return
	}

	log.Info("registered with server", "remote", conn.RemoteAddr(), "client_name", name)

	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Error("failed to clear deadline", "err", err)
		return
	}

	tunnelDataAddrs := make(map[string]string)
	var serverDataAddr string

	for _, tc := range cfg.Tunnels {
		req := protocol.RequestTunnel{
			LocalHost:     tc.LocalHost,
			LocalPort:     tc.LocalPort,
			RequestedPort: tc.RequestedPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, req); err != nil {
			log.Warn("send RequestTunnel failed", "err", err)
			return
		}

		tenv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read TunnelResp failed", "err", err)
			return
		}
		if tenv.Type != protocol.MsgTunnelResp {
			log.Warn("expected TunnelResp", "got", tenv.Type)
			return
		}

		var tresp protocol.TunnelResp
		if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
			log.Warn("decode TunnelResp failed", "err", err)
			return
		}

		if tresp.Status != "ok" {
			log.Warn("tunnel request failed", "err", tresp.Error)
			return
		}

		tunnelDataAddrs[tresp.TunnelID] = tresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = tresp.ServerDataAddr
		}
		log.Info("tunnel established",
			"tunnelID", tresp.TunnelID,
			"publicPort", tresp.PublicPort,
			"serverDataAddr", tresp.ServerDataAddr,
		)
		fmt.Printf("Tunnel: 0.0.0.0:%d -> %s:%d\n", tresp.PublicPort, tc.LocalHost, tc.LocalPort)
	}

	for _, hc := range cfg.HTTPTunnels {
		req := protocol.RequestHTTPTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPTunnel, req); err != nil {
			log.Warn("send RequestHTTPTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTP tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTP tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTP Tunnel: http://%s -> %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	for _, hc := range cfg.HTTPSTunnels {
		req := protocol.RequestHTTPSTunnel{
			Hostname:  hc.Hostname,
			LocalHost: hc.LocalHost,
			LocalPort: hc.LocalPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestHTTPSTunnel, req); err != nil {
			log.Warn("send RequestHTTPSTunnel failed", "err", err)
			return
		}

		henv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Warn("read HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}
		if henv.Type != protocol.MsgHTTPTunnelResp {
			log.Warn("expected MsgHTTPTunnelResp (HTTPS)", "got", henv.Type)
			return
		}

		var hresp protocol.HTTPTunnelResp
		if err := gob.NewDecoder(bytes.NewReader(henv.Payload)).Decode(&hresp); err != nil {
			log.Warn("decode HTTPTunnelResp (HTTPS) failed", "err", err)
			return
		}

		if hresp.Status != "ok" {
			log.Warn("HTTPS tunnel request failed", "err", hresp.Error)
			return
		}

		tunnelDataAddrs[hresp.TunnelID] = hresp.ServerDataAddr
		if serverDataAddr == "" {
			serverDataAddr = hresp.ServerDataAddr
		}
		log.Info("HTTPS tunnel registered",
			"hostname", hresp.Hostname,
			"tunnelID", hresp.TunnelID,
			"serverDataAddr", hresp.ServerDataAddr,
		)
		fmt.Printf("HTTPS Tunnel: https://%s -> %s:%d\n", hresp.Hostname, hc.LocalHost, hc.LocalPort)
	}

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	go func() {
		<-connCtx.Done()
		if ctx.Err() != nil {
			log.Info("signal received, sending disconnect")
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	}()

	const messageReadTimeout = 45 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(messageReadTimeout))
		env, err := protocol.ReadMessage(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("connection to server lost", "err", err)
			return
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				log.Warn("failed to decode Ping", "err", err)
				continue
			}
			if err := sharedWriter.WriteMsg(protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn("send Pong failed", "err", err)
				return
			}
			log.Debug("pong sent", "seq", ping.Seq)

		case protocol.MsgDisconnect:
			var disc protocol.Disconnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&disc); err != nil {
				log.Warn("failed to decode Disconnect from server", "err", err)
			} else {
				log.Info("server requested disconnect", "reason", disc.Reason)
			}
			_ = sharedWriter.WriteMsg(protocol.MsgDisconnectAck, protocol.DisconnectAck{})
			return

		case protocol.MsgOpenConnection:
			var openConn protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&openConn); err != nil {
				log.Warn("failed to decode OpenConnection", "err", err)
				continue
			}
			dataAddr, ok := tunnelDataAddrs[openConn.TunnelID]
			if !ok {
				log.Warn("received OpenConnection for unknown tunnelID",
					"tunnelID", openConn.TunnelID,
					"known_tunnels", len(tunnelDataAddrs),
				)
				continue
			}
			tunnel.HandleOpenConnection(openConn, dataAddr, log)

		case protocol.MsgSOCKSReady:
			var ready protocol.SOCKSReady
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ready); err != nil {
				log.Warn("failed to decode SOCKSReady", "err", err)
				continue
			}
			if err := clientMux.DeliverReady(ready.ConnID, ready.Success, ready.Error); err != nil {
				log.Debug("SOCKSReady deliver failed", "connID", ready.ConnID, "err", err)
			}

		case protocol.MsgSOCKSData:
			var sd protocol.SOCKSData
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sd); err != nil {
				log.Warn("failed to decode SOCKSData", "err", err)
				continue
			}
			if err := clientMux.Deliver(sd.ConnID, sd.Payload); err != nil {
				log.Debug("SOCKSData deliver failed", "connID", sd.ConnID, "err", err)
			}

		case protocol.MsgSOCKSClose:
			var sc protocol.SOCKSClose
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&sc); err != nil {
				log.Warn("failed to decode SOCKSClose", "err", err)
				continue
			}
			clientMux.DeliverClose(sc.ConnID)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}

// clientConnWriter wraps a net.Conn with a mutex so that all writes from
// concurrent goroutines are serialised. It implements socks.ControlWriter.
// The underlying connection can be swapped via SwapConn when the server reconnects.
type clientConnWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (w *clientConnWriter) WriteMsg(msgType protocol.MsgType, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn == nil {
		return fmt.Errorf("no active control connection")
	}
	return protocol.WriteMessage(w.conn, msgType, payload)
}

// SwapConn replaces the underlying connection (called on server reconnect).
func (w *clientConnWriter) SwapConn(c net.Conn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = c
}

// ClearConn sets the connection to nil (called when connection is lost).
func (w *clientConnWriter) ClearConn() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = nil
}
