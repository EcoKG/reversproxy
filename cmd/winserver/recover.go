//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// recoverLog recovers a panic and logs it with a stack trace, so a panic in a
// UI goroutine, tray handler, or refresh callback does not kill the process.
// stderr is redirected to server.log at startup.
func recoverLog(where string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "PANIC recovered in %s: %v\n%s\n", where, r, debug.Stack())
	}
}
