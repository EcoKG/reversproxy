//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32      = syscall.NewLazyDLL("user32.dll")
	messageBoxW = user32.NewProc("MessageBoxW")
)

// showError displays a Windows error dialog box.
// Safe to call before systray is initialized and from any thread.
func showError(title, msg string) { messageBox(title, msg, 0x10 /* MB_ICONERROR */) }

// showInfo displays a Windows information dialog box.
func showInfo(title, msg string) { messageBox(title, msg, 0x40 /* MB_ICONINFORMATION */) }

func messageBox(title, msg string, uType uintptr) {
	t, err1 := syscall.UTF16PtrFromString(title)
	m, err2 := syscall.UTF16PtrFromString(msg)
	if err1 != nil || err2 != nil {
		return
	}
	// MessageBoxW(hwnd, lpText, lpCaption, uType)
	_, _, _ = messageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		uType,
	)
}
