//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/lxn/walk"
)

var (
	iconOnce   sync.Once
	cachedIcon *walk.Icon
)

// windowIcon returns the *walk.Icon used for the console/settings title bars and
// taskbar entries, derived once from the embedded .ico (written to a temp file
// because walk loads icons from a path or resource id). Returns nil on failure,
// in which case windows simply use no custom icon.
func windowIcon() *walk.Icon {
	iconOnce.Do(func() {
		p := filepath.Join(os.TempDir(), "reversproxy-winserver.ico")
		if err := os.WriteFile(p, iconData, 0644); err != nil {
			return
		}
		if ic, err := walk.NewIconFromFile(p); err == nil {
			cachedIcon = ic
		}
	})
	return cachedIcon
}
