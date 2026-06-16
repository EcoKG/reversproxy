//go:build windows

// Package main implements a Windows system-tray GUI wrapper for the reversproxy
// SERVER. It runs the full server (data listener, HTTP/HTTPS proxies, admin API,
// client dial loops) in the user session and exposes status + control through a
// tray icon, a native management console (lxn/walk), and a settings dialog.
//
// A Windows service runs in session 0 and cannot show a tray icon, so this app
// is meant to run interactively (launch at logon via the tray "자동 실행" toggle)
// instead of as the NSSM service.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/EcoKG/reversproxy/internal/app"
	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/getlantern/systray"
)

//go:embed assets/icon.ico
var iconData []byte

var mStatusItem *systray.MenuItem

func main() {
	// Only one instance per user session (prevents double port binding).
	acquireSingleInstance()
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("Reversproxy Server")
	systray.SetTooltip("Reversproxy 서버")

	mStatus := systray.AddMenuItem("상태: 시작 중...", "서버 상태")
	mStatus.Disable()
	systray.AddSeparator()
	mRestart := systray.AddMenuItem("서버 재시작", "리스너·클라이언트 dial 루프 재시작")
	mConsole := systray.AddMenuItem("관리 콘솔 열기", "접속 클라이언트·터널·트래픽 통계")
	mSettings := systray.AddMenuItem("설정", "서버 설정 편집")
	mConfigFile := systray.AddMenuItem("설정 파일 열기 (고급)", "server.yaml 직접 편집")
	mLogs := systray.AddMenuItem("로그 파일 열기", "server.log 열기")
	mAutostart := systray.AddMenuItemCheckbox("로그온 시 자동 실행", "Windows 로그온 시 자동 시작", isAutoStartEnabled())
	systray.AddSeparator()
	mAbout := systray.AddMenuItem("정보(About)", "버전 정보")
	mQuit := systray.AddMenuItem("종료", "서버 종료")

	mStatusItem = mStatus

	exeDir := "."
	if p, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(p)
	}
	configPath = filepath.Join(exeDir, "server.yaml")
	logPath = filepath.Join(exeDir, "server.log")

	// Rotate an oversized log before opening, then route stdout/stderr (no console
	// in GUI mode) to the file so the log pane and '로그 파일 열기' work.
	rotateLogIfLarge(logPath)
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		os.Stdout = lf
		os.Stderr = lf
	}
	fmt.Fprintf(os.Stdout, "{\"msg\":\"winserver starting\",\"version\":%q,\"commit\":%q,\"build\":%q}\n",
		Version, Commit, BuildTime)

	// Create a default config if missing.
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		const tmpl = "data_addr: \":8444\"\nhttp_addr: \":8080\"\nhttps_addr: \":8445\"\nadmin_addr: \":9090\"\nauth_token: \"changeme\"\nlog_level: \"info\"\ninsecure: true\nallow_private_targets: true\nclients: []\n"
		_ = os.WriteFile(configPath, []byte(tmpl), 0644)
		openPath(configPath)
	}

	startServer()

	for {
		select {
		case <-mRestart.ClickedCh:
			guard("restart", restartServer)
		case <-mConsole.ClickedCh:
			guard("console", openConsole)
		case <-mSettings.ClickedCh:
			guard("settings", openSettingsDialog)
		case <-mConfigFile.ClickedCh:
			guard("configfile", func() { openPath(configPath) })
		case <-mLogs.ClickedCh:
			guard("logs", func() { openPath(logPath) })
		case <-mAbout.ClickedCh:
			guard("about", showAbout)
		case <-mAutostart.ClickedCh:
			guard("autostart", func() {
				enable := !mAutostart.Checked()
				if err := setAutoStart(enable); err != nil {
					showError("Reversproxy", "자동 실행 설정 실패:\n"+err.Error())
				} else if enable {
					mAutostart.Check()
				} else {
					mAutostart.Uncheck()
				}
			})
		case <-mQuit.ClickedCh:
			stopServer()
			time.Sleep(700 * time.Millisecond)
			systray.Quit()
			return
		}
	}
}

func onExit() {}

// guard runs a tray-menu action with panic recovery so a panic cannot kill the
// menu loop (C03).
func guard(where string, fn func()) {
	defer recoverLog("menu." + where)
	fn()
}

// startServer loads the config and runs the server in a fresh context. It waits
// briefly for an immediate listener-bind failure before claiming "실행 중".
func startServer() {
	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		showError("Reversproxy", "설정 파일 오류:\n"+err.Error())
		setStatus("상태: 설정 오류")
		return
	}

	a, err := app.NewServerApp(cfg)
	if err != nil {
		showError("Reversproxy",
			"서버 초기화 실패:\n"+err.Error()+
				"\n\n설정에서 강한 auth_token을 지정하거나, 개발용이라면 insecure: true 를 추가하세요.")
		setStatus("상태: 시작 실패")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	setApp(a, cancel)

	startErr := make(chan error, 1)
	go func() {
		defer recoverLog("server.Start")
		// Start blocks until ctx is cancelled; it returns early only on a
		// startup failure (e.g. a listen address already in use).
		err := a.Start(ctx)
		select {
		case startErr <- err:
		default:
		}
		clearRunningIf(a)
		if err != nil && ctx.Err() == nil {
			setStatus("상태: 시작 실패 (포트 충돌?)")
			showError("Reversproxy",
				"서버 시작 실패:\n"+err.Error()+
					"\n\n기존 NSSM 서비스가 같은 포트를 점유 중일 수 있습니다. 서비스를 중지하세요.")
		}
	}()

	// Catch an immediate bind error before claiming "실행 중".
	select {
	case e := <-startErr:
		if e != nil {
			return // failure already surfaced by the goroutine
		}
		setStatus("상태: 실행 중")
	case <-time.After(350 * time.Millisecond):
		setStatus("상태: 실행 중")
	}
	refreshConsole()
}

// stopServer cancels the running server.
func stopServer() {
	if c := getCancel(); c != nil {
		c()
	}
	markStopped()
}

// restartServer stops the server and starts a fresh instance after a short delay
// that lets the listeners release their ports.
func restartServer() {
	stopServer()
	setStatus("상태: 재시작 중...")
	go func() {
		defer recoverLog("restart")
		time.Sleep(900 * time.Millisecond)
		startServer()
		refreshConsole()
	}()
}

func setStatus(text string) {
	if mStatusItem != nil {
		mStatusItem.SetTitle(text)
	}
	systray.SetTooltip("Reversproxy 서버 — " + text)
}

// openPath opens a file with the system default handler on Windows.
func openPath(path string) {
	_ = exec.Command("cmd", "/c", "start", "", path).Start()
}
