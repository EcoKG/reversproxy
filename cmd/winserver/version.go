//go:build windows

package main

import "fmt"

// Build metadata, stamped via -ldflags "-X main.Version=... -X main.Commit=... -X main.BuildTime=...".
var (
	Version   = "dev"
	Commit    = ""
	BuildTime = ""
)

// versionString is a compact version for tooltips/headers.
func versionString() string {
	if Commit != "" {
		return Version + " (" + Commit + ")"
	}
	return Version
}

// aboutText is the body of the About dialog.
func aboutText() string {
	return fmt.Sprintf(
		"Reversproxy Server\n\n버전 : %s\n커밋 : %s\n빌드 : %s\n\nhttps://github.com/EcoKG/reversproxy",
		Version, valOr(Commit, "unknown"), valOr(BuildTime, "unknown"),
	)
}

// showAbout shows the About box (Win32 MessageBox — safe from any thread).
func showAbout() {
	showInfo("Reversproxy Server 정보", aboutText())
}

func valOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
