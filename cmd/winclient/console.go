//go:build windows

package main

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
)

// Console window state. consoleMW is non-nil only while the window is open; it
// is read from the tray thread (refreshConsole) so access is guarded.
var (
	consoleMu         sync.Mutex
	consoleMW         *walk.MainWindow
	consoleStatusEdit *walk.TextEdit
	consoleLogEdit    *walk.TextEdit
)

// openConsole opens the management console window, or brings it to the front if
// it is already open. The window runs on its own OS-locked thread because walk
// requires UI-thread affinity.
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var mw *walk.MainWindow
	var statusEdit, logEdit *walk.TextEdit

	decl := dec.MainWindow{
		AssignTo: &mw,
		Title:    "Reversproxy 관리 콘솔",
		Size:     dec.Size{Width: 660, Height: 600},
		MinSize:  dec.Size{Width: 460, Height: 400},
		Layout:   dec.VBox{},
		Children: []dec.Widget{
			dec.GroupBox{
				Title:  "상태",
				Layout: dec.VBox{},
				Children: []dec.Widget{
					dec.TextEdit{
						AssignTo: &statusEdit,
						ReadOnly: true,
						VScroll:  true,
						Font:     dec.Font{Family: "Consolas", PointSize: 10},
					},
				},
			},
			dec.GroupBox{
				Title:  "최근 로그",
				Layout: dec.VBox{},
				Children: []dec.Widget{
					dec.TextEdit{
						AssignTo: &logEdit,
						ReadOnly: true,
						VScroll:  true,
						HScroll:  true,
						Font:     dec.Font{Family: "Consolas", PointSize: 9},
					},
				},
			},
			dec.Composite{
				Layout: dec.HBox{},
				Children: []dec.Widget{
					dec.PushButton{Text: "새로고침", OnClicked: func() { refreshConsoleWidgets(statusEdit, logEdit) }},
					dec.PushButton{Text: "재연결", OnClicked: func() { reconnectTunnel() }},
					dec.PushButton{Text: "설정", OnClicked: func() { openSettingsDialog() }},
					dec.PushButton{Text: "설정 파일 열기", OnClicked: func() { openConfigFile(configPath) }},
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

	consoleMu.Lock()
	consoleMW = mw
	consoleStatusEdit = statusEdit
	consoleLogEdit = logEdit
	consoleMu.Unlock()

	refreshConsoleWidgets(statusEdit, logEdit)

	// Auto-refresh while open.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				mw.Synchronize(func() { refreshConsoleWidgets(statusEdit, logEdit) })
			}
		}
	}()

	mw.Show()
	mw.Run() // blocks until the window is closed

	close(stop)
	consoleMu.Lock()
	consoleMW = nil
	consoleStatusEdit = nil
	consoleLogEdit = nil
	consoleMu.Unlock()
}

// refreshConsole is invoked from the tray thread (updateStatus). It posts a UI
// update to the console window if it is open; otherwise it is a no-op.
func refreshConsole() {
	consoleMu.Lock()
	mw := consoleMW
	se := consoleStatusEdit
	le := consoleLogEdit
	consoleMu.Unlock()
	if mw == nil {
		return
	}
	mw.Synchronize(func() { refreshConsoleWidgets(se, le) })
}

// refreshConsoleWidgets re-renders the status and log panes. It must run on the
// console's UI thread (via a button handler, Create, or mw.Synchronize).
func refreshConsoleWidgets(statusEdit, logEdit *walk.TextEdit) {
	if statusEdit != nil {
		_ = statusEdit.SetText(status.snapshot().summary())
	}
	if logEdit != nil {
		_, lp := status.paths()
		_ = logEdit.SetText(tailFile(lp, 200))
	}
}

// tailFile returns the last n lines of the file at path (best-effort).
func tailFile(path string, n int) string {
	if path == "" {
		return "(로그 경로가 설정되지 않았습니다)"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "(로그를 읽을 수 없습니다: " + err.Error() + ")"
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\r\n")
}
