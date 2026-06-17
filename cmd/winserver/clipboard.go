//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/lxn/walk"
)

// randomToken returns a 256-bit cryptographically random hex token suitable for
// an auth_token. Returns "" on the (practically impossible) rand failure.
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// copyToClipboard puts text on the Windows clipboard. Must be called from a UI
// thread (e.g. a button handler).
func copyToClipboard(text string) error {
	return walk.Clipboard().SetText(text)
}
