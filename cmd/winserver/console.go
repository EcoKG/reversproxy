//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
)

// Console window state. consoleMW is non-nil only while the window is open;
// it is read from the tray thread (refreshConsole) so access is guarded.
var (
	consoleMu sync.Mutex
	consoleMW *walk.MainWindow
	consoleUI *consoleWidgets
)

// consoleWidgets holds the widgets/models the refresh routine updates. It lives
// on the console's UI thread.
type consoleWidgets struct {
	header     *walk.Label
	clientTV   *walk.TableView
	tunnelTV   *walk.TableView
	clientMdl  *clientModel
	tunnelMdl  *tunnelModel
	lblTotal   *walk.Label
	lblActive  *walk.Label
	lblIn      *walk.Label
	lblOut     *walk.Label
	lblProxies *walk.Label
	logEdit    *walk.TextEdit
}

// openConsole opens the management console window, or brings it to the front if
// already open. It runs on its own OS-locked thread (walk UI-thread affinity).
func openConsole() {
	consoleMu.Lock()
	mw := consoleMW
	consoleMu.Unlock()
	if mw != nil {
		mw.Synchronize(func() { mw.Show() })
		return
	}
	go runConsole()
}

func runConsole() {
	defer recoverLog("runConsole")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	clientMdl := &clientModel{}
	tunnelMdl := &tunnelModel{}
	ui := &consoleWidgets{clientMdl: clientMdl, tunnelMdl: tunnelMdl}

	var mw *walk.MainWindow

	decl := dec.MainWindow{
		AssignTo: &mw,
		Title:    "Reversproxy 서버 관리 콘솔",
		Size:     dec.Size{Width: 820, Height: 640},
		MinSize:  dec.Size{Width: 560, Height: 440},
		Layout:   dec.VBox{},
		Children: []dec.Widget{
			dec.Label{AssignTo: &ui.header, Text: "…"},
			dec.TabWidget{
				Pages: []dec.TabPage{
					{
						Title:  "클라이언트",
						Layout: dec.VBox{},
						Children: []dec.Widget{
							dec.TableView{
								AssignTo:         &ui.clientTV,
								Model:            clientMdl,
								AlternatingRowBG: true,
								Columns: []dec.TableViewColumn{
									{Title: "이름", Width: 150},
									{Title: "주소", Width: 180},
									{Title: "접속 시각", Width: 90},
									{Title: "경과", Width: 110},
									{Title: "터널", Width: 60},
								},
							},
							dec.Composite{
								Layout: dec.HBox{},
								Children: []dec.Widget{
									dec.PushButton{Text: "선택 클라이언트 끊기", OnClicked: ui.onDisconnectSelected},
									dec.HSpacer{},
								},
							},
						},
					},
					{
						Title:  "터널",
						Layout: dec.VBox{},
						Children: []dec.Widget{
							dec.TableView{
								AssignTo:         &ui.tunnelTV,
								Model:            tunnelMdl,
								AlternatingRowBG: true,
								Columns: []dec.TableViewColumn{
									{Title: "종류", Width: 70},
									{Title: "공개/호스트", Width: 220},
									{Title: "로컬 대상", Width: 200},
									{Title: "In/Out", Width: 100},
								},
							},
						},
					},
					{
						Title:  "통계",
						Layout: dec.Grid{Columns: 2},
						Children: []dec.Widget{
							dec.Label{Text: "총 연결"}, dec.Label{AssignTo: &ui.lblTotal, Text: "-"},
							dec.Label{Text: "활성 연결"}, dec.Label{AssignTo: &ui.lblActive, Text: "-"},
							dec.Label{Text: "수신(In)"}, dec.Label{AssignTo: &ui.lblIn, Text: "-"},
							dec.Label{Text: "송신(Out)"}, dec.Label{AssignTo: &ui.lblOut, Text: "-"},
							dec.Label{Text: "프록시"}, dec.Label{AssignTo: &ui.lblProxies, Text: "-"},
						},
					},
					{
						Title:  "로그",
						Layout: dec.VBox{},
						Children: []dec.Widget{
							dec.TextEdit{
								AssignTo: &ui.logEdit,
								ReadOnly: true,
								VScroll:  true,
								HScroll:  true,
								Font:     dec.Font{Family: "Consolas", PointSize: 9},
							},
						},
					},
				},
			},
			dec.Composite{
				Layout: dec.HBox{},
				Children: []dec.Widget{
					dec.PushButton{Text: "새로고침", OnClicked: func() { ui.refresh() }},
					dec.PushButton{Text: "서버 재시작", OnClicked: func() { restartServer() }},
					dec.PushButton{Text: "설정", OnClicked: func() { openSettingsDialog() }},
					dec.PushButton{Text: "Admin URL 복사", OnClicked: ui.onCopyAdminURL},
					dec.PushButton{Text: "접속 정보 복사", OnClicked: ui.onCopyConnInfo},
					dec.PushButton{Text: "정보", OnClicked: func() { showAbout() }},
					dec.HSpacer{},
					dec.PushButton{Text: "닫기", OnClicked: func() {
						if mw != nil {
							mw.Close()
						}
					}},
				},
			},
		},
	}

	if err := decl.Create(); err != nil {
		showError("Reversproxy", "콘솔 창 생성 실패:\n"+err.Error())
		return
	}
	if ic := windowIcon(); ic != nil {
		_ = mw.SetIcon(ic)
	}

	consoleMu.Lock()
	consoleMW = mw
	consoleUI = ui
	consoleMu.Unlock()

	ui.refresh()

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				mw.Synchronize(func() {
					defer recoverLog("console.refresh")
					ui.refresh()
				})
			}
		}
	}()

	mw.Show()
	mw.Run()

	close(stop)
	consoleMu.Lock()
	consoleMW = nil
	consoleUI = nil
	consoleMu.Unlock()
}

// refresh re-renders all panes. Must run on the console UI thread.
func (ui *consoleWidgets) refresh() {
	header, _ := serverHeaderText()
	if ui.header != nil {
		_ = ui.header.SetText(header)
	}

	a, running := getApp()
	if running && a != nil {
		ui.clientMdl.setItems(snapshotClients(a))
		ui.tunnelMdl.setItems(snapshotTunnels(a))
		gs := a.GlobalStats()
		setLabel(ui.lblTotal, fmt.Sprintf("%d", gs.TotalConnections.Load()))
		setLabel(ui.lblActive, fmt.Sprintf("%d", gs.ActiveConnections.Load()))
		setLabel(ui.lblIn, humanBytes(gs.BytesIn.Load()))
		setLabel(ui.lblOut, humanBytes(gs.BytesOut.Load()))
		cfg := a.Config()
		setLabel(ui.lblProxies, fmt.Sprintf("HTTP %s · HTTPS %s · TLS검증 %s · 내부망 %s",
			dash(cfg.HTTPAddr), dash(cfg.HTTPSAddr), onoff(!cfg.Insecure), onoff(cfg.AllowPrivateTargets)))
	} else {
		ui.clientMdl.setItems(nil)
		ui.tunnelMdl.setItems(nil)
		setLabel(ui.lblTotal, "-")
		setLabel(ui.lblActive, "-")
		setLabel(ui.lblIn, "-")
		setLabel(ui.lblOut, "-")
		setLabel(ui.lblProxies, "-")
	}

	if ui.logEdit != nil {
		_ = ui.logEdit.SetText(tailFile(logPath, 250))
		// Auto-scroll to the newest line.
		n := utf16Len(ui.logEdit.Text())
		ui.logEdit.SetTextSelection(n, n)
		ui.logEdit.ScrollToCaret()
	}
}

func (ui *consoleWidgets) onDisconnectSelected() {
	if ui.clientTV == nil {
		return
	}
	idx := ui.clientTV.CurrentIndex()
	id := ui.clientMdl.idAt(idx)
	if id == "" {
		showInfo("Reversproxy", "끊을 클라이언트를 먼저 선택하세요.")
		return
	}
	name := ui.clientMdl.items[idx].Name
	if walk.MsgBox(consoleMW, "클라이언트 끊기",
		fmt.Sprintf("클라이언트 '%s' 연결을 끊을까요?", name),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	a, running := getApp()
	if running && a != nil {
		a.Registry().Disconnect(id)
	}
	ui.refresh()
}

func (ui *consoleWidgets) onCopyAdminURL() {
	a, running := getApp()
	if !running || a == nil || a.Config().AdminAddr == "" {
		showInfo("Reversproxy", "Admin API가 비활성 상태입니다.")
		return
	}
	_ = copyToClipboard("http://" + loopbackURL(a.Config().AdminAddr))
}

func (ui *consoleWidgets) onCopyConnInfo() {
	a, running := getApp()
	if !running || a == nil {
		return
	}
	cfg := a.Config()
	info := fmt.Sprintf("Reversproxy 서버\ndata: %s\nHTTP: %s\nHTTPS: %s\nAdmin: %s\n버전: %s",
		cfg.DataAddr, dash(cfg.HTTPAddr), dash(cfg.HTTPSAddr), dash(cfg.AdminAddr), versionString())
	_ = copyToClipboard(info)
}

// refreshConsole posts a UI update from the tray thread if the console is open.
func refreshConsole() {
	consoleMu.Lock()
	mw := consoleMW
	ui := consoleUI
	consoleMu.Unlock()
	if mw == nil || ui == nil {
		return
	}
	mw.Synchronize(func() {
		defer recoverLog("refreshConsole")
		ui.refresh()
	})
}

func setLabel(l *walk.Label, text string) {
	if l != nil {
		_ = l.SetText(text)
	}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// loopbackURL turns a ":9090" / "0.0.0.0:9090" admin addr into a usable
// 127.0.0.1:9090 URL host.
func loopbackURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	return addr
}

// tailFile returns the last n lines of the file at path WITHOUT reading the whole
// file: it seeks to the last 64 KiB. Best-effort.
func tailFile(path string, n int) string {
	if path == "" {
		return "(로그 경로가 설정되지 않았습니다)"
	}
	f, err := os.Open(path)
	if err != nil {
		return "(로그를 읽을 수 없습니다: " + err.Error() + ")"
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const window = 64 * 1024
	start := int64(0)
	partial := false
	if fi.Size() > window {
		start = fi.Size() - window
		partial = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if partial && len(lines) > 0 {
		lines = lines[1:] // drop the first, possibly-truncated line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\r\n")
}
