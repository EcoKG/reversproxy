//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// singleInstanceHandle is kept for the process lifetime so the named mutex is
// not released early.
var singleInstanceHandle windows.Handle

// acquireSingleInstance ensures only one winserver runs per user session.
// A second instance would double-bind the listener ports. Uses a session-scoped
// (Local\) mutex to match the per-user (HKCU) autostart. Fail-open on unexpected
// errors so a transient failure never blocks startup.
func acquireSingleInstance() {
	name, err := windows.UTF16PtrFromString(`Local\ReversproxyServerWinSrv`)
	if err != nil {
		return
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		showInfo("Reversproxy", "Reversproxy 서버가 이미 실행 중입니다.")
		os.Exit(0)
	}
	if err != nil {
		return // unexpected error: proceed (fail-open)
	}
	singleInstanceHandle = h
}
