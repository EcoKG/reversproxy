//go:build windows

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/EcoKG/reversproxy/internal/app"
)

// All live server state is guarded by one mutex (C26) so the console (reader)
// and start/stop/restart (writers) cannot race during an app swap.
var (
	srvMu      sync.RWMutex
	currentApp *app.ServerApp
	appCancel  context.CancelFunc
	srvRunning bool

	configPath string
	logPath    string
)

// setApp atomically records a freshly started server, its canceller, and marks
// it running.
func setApp(a *app.ServerApp, cancel context.CancelFunc) {
	srvMu.Lock()
	currentApp = a
	appCancel = cancel
	srvRunning = true
	srvMu.Unlock()
}

// clearRunningIf marks the server stopped ONLY if a is still the current app.
// A slow previous Start goroutine returning after a restart must not clear the
// new instance's running state (compare-and-clear).
func clearRunningIf(a *app.ServerApp) {
	srvMu.Lock()
	if currentApp == a {
		srvRunning = false
	}
	srvMu.Unlock()
}

// markStopped forces the running flag false (used by an explicit stop for
// immediate UI feedback).
func markStopped() {
	srvMu.Lock()
	srvRunning = false
	srvMu.Unlock()
}

func getApp() (*app.ServerApp, bool) {
	srvMu.RLock()
	defer srvMu.RUnlock()
	return currentApp, srvRunning
}

func getCancel() context.CancelFunc {
	srvMu.RLock()
	defer srvMu.RUnlock()
	return appCancel
}

// serverHeaderText is the compact one/two-line summary shown above the console
// tabs and reused in the tray tooltip.
func serverHeaderText() (text string, running bool) {
	defer func() { _ = recover() }() // read-side defence (C27)
	a, run := getApp()
	if !run || a == nil {
		return "■ 중지됨   ·   v" + versionString(), false
	}
	cfg := a.Config()
	text = fmt.Sprintf("● 실행 중   ·   data %s · HTTP %s · HTTPS %s · Admin %s   ·   v%s",
		cfg.DataAddr, dash(cfg.HTTPAddr), dash(cfg.HTTPSAddr), dash(cfg.AdminAddr), versionString())
	return text, true
}

func dash(s string) string {
	if s == "" {
		return "off"
	}
	return s
}

func onoff(v bool) string {
	if v {
		return "켜짐"
	}
	return "꺼짐"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
