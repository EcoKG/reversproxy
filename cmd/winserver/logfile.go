//go:build windows

package main

import "os"

// maxLogBytes caps server.log; on startup an oversized log is rotated once.
const maxLogBytes = 5 * 1024 * 1024

// rotateLogIfLarge renames path to path+".1" when it exceeds the cap. It runs
// BEFORE the log file is (re)opened/redirected — live re-pointing is unsafe
// because slog captures the fd at construction time. Best-effort.
func rotateLogIfLarge(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}
