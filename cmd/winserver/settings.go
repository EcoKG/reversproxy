//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
	"gopkg.in/yaml.v3"

	"github.com/EcoKG/reversproxy/internal/config"
)

var (
	settingsMu sync.Mutex
	settingsMW *walk.MainWindow
)

// openSettingsDialog opens the server settings window, or focuses it if open.
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

	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		showError("Reversproxy", "설정 파일을 읽을 수 없어 GUI 편집을 열 수 없습니다.\n'설정 파일 열기'로 직접 수정하세요.\n\n"+err.Error())
		return
	}

	var mw *walk.MainWindow
	var (
		dataEdit, httpEdit, httpsEdit, adminEdit *walk.LineEdit
		tokenEdit, adminTokenEdit, logLevelEdit  *walk.LineEdit
		insecureCheck, allowPrivateCheck         *walk.CheckBox
	)

	decl := dec.MainWindow{
		AssignTo: &mw,
		Title:    "Reversproxy 서버 설정",
		Size:     dec.Size{Width: 500, Height: 540},
		MinSize:  dec.Size{Width: 440, Height: 480},
		Layout:   dec.VBox{},
		Children: []dec.Widget{
			dec.GroupBox{
				Title:  "리슨 주소",
				Layout: dec.Grid{Columns: 2},
				Children: []dec.Widget{
					dec.Label{Text: "데이터 주소"},
					dec.LineEdit{AssignTo: &dataEdit, Text: cfg.DataAddr, CueBanner: ":8444"},
					dec.Label{Text: "HTTP 프록시 (비우면 끔)"},
					dec.LineEdit{AssignTo: &httpEdit, Text: cfg.HTTPAddr, CueBanner: ":8080"},
					dec.Label{Text: "HTTPS 프록시 (비우면 끔)"},
					dec.LineEdit{AssignTo: &httpsEdit, Text: cfg.HTTPSAddr, CueBanner: ":8445"},
					dec.Label{Text: "Admin API (비우면 끔)"},
					dec.LineEdit{AssignTo: &adminEdit, Text: cfg.AdminAddr, CueBanner: ":9090"},
				},
			},
			dec.GroupBox{
				Title:  "보안",
				Layout: dec.Grid{Columns: 2},
				Children: []dec.Widget{
					dec.Label{Text: "인증 토큰 (클라이언트와 동일)"},
					dec.Composite{
						Layout: dec.HBox{MarginsZero: true},
						Children: []dec.Widget{
							dec.LineEdit{AssignTo: &tokenEdit, Text: cfg.AuthToken, PasswordMode: true},
							dec.PushButton{Text: "생성", MaxSize: dec.Size{Width: 60}, OnClicked: func() {
								if t := randomToken(); t != "" {
									_ = tokenEdit.SetText(t)
								}
							}},
						},
					},
					dec.Label{Text: "Admin 토큰 (비우면 무인증)"},
					dec.LineEdit{AssignTo: &adminTokenEdit, Text: cfg.AdminToken, PasswordMode: true},
					dec.Label{Text: "로그 레벨"},
					dec.LineEdit{AssignTo: &logLevelEdit, Text: cfg.LogLevel, CueBanner: "info"},
					dec.Label{Text: "TLS 검증 건너뜀 (insecure)"},
					dec.CheckBox{AssignTo: &insecureCheck, Checked: cfg.Insecure, Text: "개발용: changeme·자체서명 허용"},
					dec.Label{Text: "내부망(사설 IP) dial 허용"},
					dec.CheckBox{AssignTo: &allowPrivateCheck, Checked: cfg.AllowPrivateTargets, Text: "allow_private_targets"},
				},
			},
			dec.Label{Text: "클라이언트 목록(clients) 등은 '설정 파일 열기'로 편집하세요."},
			dec.Composite{
				Layout: dec.HBox{},
				Children: []dec.Widget{
					dec.PushButton{Text: "설정 파일 열기", OnClicked: func() { openPath(configPath) }},
					dec.HSpacer{},
					dec.PushButton{Text: "저장", OnClicked: func() {
						cfg.DataAddr = dataEdit.Text()
						cfg.HTTPAddr = httpEdit.Text()
						cfg.HTTPSAddr = httpsEdit.Text()
						cfg.AdminAddr = adminEdit.Text()
						cfg.AuthToken = tokenEdit.Text()
						cfg.AdminToken = adminTokenEdit.Text()
						cfg.LogLevel = logLevelEdit.Text()
						cfg.Insecure = insecureCheck.Checked()
						cfg.AllowPrivateTargets = allowPrivateCheck.Checked()

						// Validate before saving (C29): addresses parse, and the
						// security policy (weak/empty token unless insecure) holds —
						// reuse ValidateSecurity so the GUI never drifts from the server.
						if msg := validateServerForm(cfg); msg != "" {
							walk.MsgBox(mw, "설정 오류", msg, walk.MsgBoxIconExclamation)
							return
						}

						if err := saveServerConfig(configPath, cfg); err != nil {
							walk.MsgBox(mw, "저장 실패", err.Error(), walk.MsgBoxIconError)
							return
						}
						walk.MsgBox(mw, "저장됨",
							"설정을 저장했습니다.\n트레이 메뉴의 '서버 재시작' 후 적용됩니다.",
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
	if ic := windowIcon(); ic != nil {
		_ = mw.SetIcon(ic)
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

// validateServerForm returns a non-empty error message if the populated config
// is invalid: any non-empty listen address must parse, and the security policy
// (no weak/empty auth_token unless insecure) must hold. ValidateSecurity is the
// server's own rule, reused so the GUI cannot drift from it.
func validateServerForm(cfg *config.ServerConfig) string {
	for label, addr := range map[string]string{
		"데이터 주소": cfg.DataAddr, "HTTP 프록시": cfg.HTTPAddr,
		"HTTPS 프록시": cfg.HTTPSAddr, "Admin API": cfg.AdminAddr,
	} {
		if addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Sprintf("%s 주소가 올바르지 않습니다 (host:port): %q", label, addr)
		}
	}
	if err := cfg.ValidateSecurity(); err != nil {
		return err.Error()
	}
	return ""
}

// saveServerConfig writes cfg to path as YAML. The clients list and other fields
// loaded from disk are preserved (the form edits only the scalar fields above).
func saveServerConfig(path string, cfg *config.ServerConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
