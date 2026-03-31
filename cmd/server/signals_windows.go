//go:build windows

package main

import "os"

// shutdownSignals lists OS signals that trigger graceful shutdown.
// Windows does not have SIGTERM; only os.Interrupt (Ctrl+C) is used.
var shutdownSignals = []os.Signal{os.Interrupt}
