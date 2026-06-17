//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// Windows per-user logon autostart is configured via the HKCU Run key, which
// requires no administrator rights.
const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "Reversproxy"
)

// isAutoStartEnabled reports whether the winclient is registered to launch at
// logon for the current user.
func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	return err == nil && v != ""
}

// setAutoStart enables or disables launching the winclient at logon for the
// current user (HKCU Run key).
func setAutoStart(enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	if !enable {
		if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("remove autostart entry: %w", err)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	// Quote the path so a path containing spaces is launched correctly.
	if err := k.SetStringValue(runValueName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("write autostart entry: %w", err)
	}
	return nil
}
