//go:build unix

package main

import (
	"os"
	"syscall"
)

// shutdownSignals lists OS signals that trigger graceful shutdown.
// On Unix we also listen for SIGTERM (sent by systemd / kill).
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
