//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
)

// liveStatus holds the winclient's current runtime state for display in the
// management console. It is updated by the tunnel loop and read by the console
// window, so all access is guarded by a mutex.
type liveStatus struct {
	mu          sync.RWMutex
	state       connStateVal
	serverAddr  string    // remote address of the connected server (when connected)
	connectedAt time.Time // when the current server connection was established
	cfg         *config.ClientConfig
	configPath  string
	logPath     string
}

// status is the process-wide live status, shared between the tray loop and the
// management console.
var status = &liveStatus{}

// setState records the connection state and (optionally) the connected server
// address. Pass an empty serverAddr to leave it unchanged for non-connected states.
func (s *liveStatus) setState(st connStateVal, serverAddr string) {
	s.mu.Lock()
	s.state = st
	switch st {
	case stateConnected:
		if serverAddr != "" {
			s.serverAddr = serverAddr
		}
		if s.connectedAt.IsZero() {
			// connectedAt is set by the caller via setConnectedAt; keep any value.
		}
	default:
		s.serverAddr = ""
		s.connectedAt = time.Time{}
	}
	s.mu.Unlock()
}

// setConnected records a freshly established server connection.
func (s *liveStatus) setConnected(serverAddr string, at time.Time) {
	s.mu.Lock()
	s.state = stateConnected
	s.serverAddr = serverAddr
	s.connectedAt = at
	s.mu.Unlock()
}

// setConfig records the active configuration and its paths.
func (s *liveStatus) setConfig(cfg *config.ClientConfig, configPath, logPath string) {
	s.mu.Lock()
	s.cfg = cfg
	if configPath != "" {
		s.configPath = configPath
	}
	if logPath != "" {
		s.logPath = logPath
	}
	s.mu.Unlock()
}

// paths returns the config and log file paths.
func (s *liveStatus) paths() (configPath, logPath string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configPath, s.logPath
}

// statusSnapshot is an immutable view of liveStatus for rendering.
type statusSnapshot struct {
	State       connStateVal
	ServerAddr  string
	ConnectedAt time.Time
	Cfg         *config.ClientConfig
	ConfigPath  string
	LogPath     string
}

// snapshot returns a consistent copy of the current status.
func (s *liveStatus) snapshot() statusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return statusSnapshot{
		State:       s.state,
		ServerAddr:  s.serverAddr,
		ConnectedAt: s.connectedAt,
		Cfg:         s.cfg,
		ConfigPath:  s.configPath,
		LogPath:     s.logPath,
	}
}

// stateText returns a human-readable Korean label for a connection state.
func stateText(st connStateVal) string {
	switch st {
	case stateConnected:
		return "연결됨"
	case stateConnecting:
		return "대기 중 (서버 연결 기다리는 중)"
	default:
		return "연결 안됨"
	}
}

// summary renders the snapshot as a human-readable multi-line report used by the
// console's status pane.
func (snap statusSnapshot) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "연결 상태 : %s\r\n", stateText(snap.State))
	if snap.State == stateConnected {
		fmt.Fprintf(&b, "서버 주소 : %s\r\n", snap.ServerAddr)
		if !snap.ConnectedAt.IsZero() {
			fmt.Fprintf(&b, "연결 시각 : %s (%s 경과)\r\n",
				snap.ConnectedAt.Format("2006-01-02 15:04:05"),
				time.Since(snap.ConnectedAt).Round(time.Second))
		}
	}

	cfg := snap.Cfg
	if cfg == nil {
		b.WriteString("\r\n설정이 로드되지 않았습니다.\r\n")
		return b.String()
	}

	fmt.Fprintf(&b, "클라이언트 이름 : %s\r\n", cfg.Name)
	fmt.Fprintf(&b, "리슨 주소 : %s\r\n", cfg.ListenAddr)
	fmt.Fprintf(&b, "TLS 검증 : %s\r\n", boolText(!cfg.Insecure))

	b.WriteString("\r\n── 프록시 ──\r\n")
	if cfg.SOCKSAddr != "" {
		auth := "인증 없음"
		if cfg.SOCKSUser != "" {
			auth = "인증 사용"
		}
		fmt.Fprintf(&b, "SOCKS5 : %s (%s)\r\n", proxyBoundAddr(cfg.SOCKSAddr), auth)
	} else {
		b.WriteString("SOCKS5 : 비활성\r\n")
	}
	if cfg.HTTPProxyAddr != "" {
		auth := "인증 없음"
		if cfg.HTTPProxyUser != "" {
			auth = "인증 사용"
		}
		fmt.Fprintf(&b, "HTTP CONNECT : %s (%s)\r\n", proxyBoundAddr(cfg.HTTPProxyAddr), auth)
	} else {
		b.WriteString("HTTP CONNECT : 비활성\r\n")
	}

	b.WriteString("\r\n── 터널 ──\r\n")
	if len(cfg.Tunnels) == 0 {
		b.WriteString("등록된 터널 없음\r\n")
	}
	for _, t := range cfg.Tunnels {
		typ := t.Type
		if typ == "" {
			typ = "tcp"
		}
		switch typ {
		case "http", "https":
			fmt.Fprintf(&b, "[%s] %s → %s:%d\r\n", typ, t.Hostname, t.LocalHost, t.LocalPort)
		default:
			fmt.Fprintf(&b, "[tcp] 공인포트 %d → %s:%d\r\n", t.RequestedPort, t.LocalHost, t.LocalPort)
		}
	}

	if len(cfg.PortForwards) > 0 {
		b.WriteString("\r\n── 포트 포워드 ──\r\n")
		for _, pf := range cfg.PortForwards {
			bind := pf.Bind
			if bind == "" {
				bind = "0.0.0.0"
			}
			fmt.Fprintf(&b, "%s:%d → %s:%d\r\n", bind, pf.LocalPort, pf.RemoteHost, pf.RemotePort)
		}
	}

	return b.String()
}

func boolText(v bool) string {
	if v {
		return "켜짐"
	}
	return "꺼짐"
}

// proxyBoundAddr returns the actual bound proxy address, preferring the value
// the listener reported (which resolves a ":0" port) over the configured value.
func proxyBoundAddr(configured string) string {
	return configured
}
