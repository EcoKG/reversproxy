//go:build windows

package main

import (
	"os"
	"runtime"
	"sync"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
	"gopkg.in/yaml.v3"

	"github.com/EcoKG/reversproxy/internal/config"
)

// Settings window state (single-instance guard).
var (
	settingsMu sync.Mutex
	settingsMW *walk.MainWindow
)

// openSettingsDialog opens the GUI settings window, or focuses it if already
// open. Runs on its own OS-locked thread (walk UI-thread affinity).
func openSettingsDialog() {
	settingsMu.Lock()
	mw := settingsMW
	settingsMu.Unlock()
	if mw != nil {
		mw.Synchronize(func() { mw.Show() })
		return
	}
	go runSettingsWindow()
}

func runSettingsWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Load the current config so the form is pre-filled and tunnels/port-forwards
	// are preserved on save. Fall back to defaults if the file is missing.
	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		showError("Reversproxy", "설정 파일을 읽을 수 없어 GUI 편집을 열 수 없습니다.\n'설정 파일 열기'로 직접 수정하세요.\n\n"+err.Error())
		return
	}
	if cfg == nil {
		cfg = config.DefaultClientConfig()
	}

	var mw *walk.MainWindow
	var (
		nameEdit, listenEdit, tokenEdit, logLevelEdit *walk.LineEdit
		socksAddrEdit, socksUserEdit, socksPassEdit   *walk.LineEdit
		httpAddrEdit, httpUserEdit, httpPassEdit      *walk.LineEdit
		insecureCheck                                 *walk.CheckBox
	)

	decl := dec.MainWindow{
		AssignTo: &mw,
		Title:    "Reversproxy 설정",
		Size:     dec.Size{Width: 480, Height: 520},
		MinSize:  dec.Size{Width: 420, Height: 460},
		Layout:   dec.VBox{},
		Children: []dec.Widget{
			dec.GroupBox{
				Title:  "기본",
				Layout: dec.Grid{Columns: 2},
				Children: []dec.Widget{
					dec.Label{Text: "클라이언트 이름"},
					dec.LineEdit{AssignTo: &nameEdit, Text: cfg.Name},
					dec.Label{Text: "리슨 주소"},
					dec.LineEdit{AssignTo: &listenEdit, Text: cfg.ListenAddr, CueBanner: ":8443"},
					dec.Label{Text: "인증 토큰"},
					dec.LineEdit{AssignTo: &tokenEdit, Text: cfg.AuthToken, PasswordMode: true},
					dec.Label{Text: "로그 레벨"},
					dec.LineEdit{AssignTo: &logLevelEdit, Text: cfg.LogLevel, CueBanner: "info"},
					dec.Label{Text: "TLS 검증 건너뛰기 (개발용)"},
					dec.CheckBox{AssignTo: &insecureCheck, Checked: cfg.Insecure, Text: "insecure"},
				},
			},
			dec.GroupBox{
				Title:  "SOCKS5 프록시 (비우면 비활성)",
				Layout: dec.Grid{Columns: 2},
				Children: []dec.Widget{
					dec.Label{Text: "주소"},
					dec.LineEdit{AssignTo: &socksAddrEdit, Text: cfg.SOCKSAddr, CueBanner: "127.0.0.1:1080"},
					dec.Label{Text: "사용자"},
					dec.LineEdit{AssignTo: &socksUserEdit, Text: cfg.SOCKSUser},
					dec.Label{Text: "비밀번호"},
					dec.LineEdit{AssignTo: &socksPassEdit, Text: cfg.SOCKSPass, PasswordMode: true},
				},
			},
			dec.GroupBox{
				Title:  "HTTP CONNECT 프록시 (비우면 비활성)",
				Layout: dec.Grid{Columns: 2},
				Children: []dec.Widget{
					dec.Label{Text: "주소"},
					dec.LineEdit{AssignTo: &httpAddrEdit, Text: cfg.HTTPProxyAddr, CueBanner: "127.0.0.1:8080"},
					dec.Label{Text: "사용자"},
					dec.LineEdit{AssignTo: &httpUserEdit, Text: cfg.HTTPProxyUser},
					dec.Label{Text: "비밀번호"},
					dec.LineEdit{AssignTo: &httpPassEdit, Text: cfg.HTTPProxyPass, PasswordMode: true},
				},
			},
			dec.Label{Text: "터널·포트포워드 등 고급 설정은 '설정 파일 열기'로 편집하세요."},
			dec.Composite{
				Layout: dec.HBox{},
				Children: []dec.Widget{
					dec.PushButton{Text: "설정 파일 열기", OnClicked: func() { openConfigFile(configPath) }},
					dec.HSpacer{},
					dec.PushButton{Text: "저장", OnClicked: func() {
						cfg.Name = nameEdit.Text()
						cfg.ListenAddr = listenEdit.Text()
						cfg.AuthToken = tokenEdit.Text()
						cfg.LogLevel = logLevelEdit.Text()
						cfg.Insecure = insecureCheck.Checked()
						cfg.SOCKSAddr = socksAddrEdit.Text()
						cfg.SOCKSUser = socksUserEdit.Text()
						cfg.SOCKSPass = socksPassEdit.Text()
						cfg.HTTPProxyAddr = httpAddrEdit.Text()
						cfg.HTTPProxyUser = httpUserEdit.Text()
						cfg.HTTPProxyPass = httpPassEdit.Text()

						if err := saveClientConfig(configPath, cfg); err != nil {
							walk.MsgBox(mw, "저장 실패", err.Error(), walk.MsgBoxIconError)
							return
						}
						// Reflect the new config in the live status immediately.
						status.setConfig(cfg, configPath, logPath)
						walk.MsgBox(mw, "저장됨",
							"설정을 저장했습니다.\n변경 사항은 '재연결' 후 적용됩니다.",
							walk.MsgBoxIconInformation)
						mw.Close()
					}},
					dec.PushButton{Text: "취소", OnClicked: func() { mw.Close() }},
				},
			},
		},
	}

	if err := decl.Create(); err != nil {
		showError("Reversproxy", "설정 창 생성 실패:\n"+err.Error())
		return
	}

	settingsMu.Lock()
	settingsMW = mw
	settingsMu.Unlock()

	mw.Show()
	mw.Run()

	settingsMu.Lock()
	settingsMW = nil
	settingsMu.Unlock()
}

// saveClientConfig writes cfg to path as YAML. Tunnels and port-forwards present
// in cfg are preserved (the form edits only scalar fields).
func saveClientConfig(path string, cfg *config.ClientConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
