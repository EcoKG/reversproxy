// Package filetransfer implements a small, resumable, integrity-checked file
// transfer protocol over HTTP. It is transport-agnostic: in production the
// receiver's listen address is exposed to the peer through the existing proxy
// tunnel, so a "send" on one side lands in the other side's drop folder.
//
// Wire protocol — single endpoint, path /upload:
//
//	HEAD  — query how many bytes the server already holds for a file:
//	        request header  X-Filename
//	        response header X-Offset   (bytes currently held; resume point)
//
//	PUT   — upload (a slice of) a file starting at X-Offset:
//	        request headers
//	          X-Filename    base file name (no path components)
//	          X-Total       total file size in bytes (decimal)
//	          X-Sha256      hex-encoded SHA-256 of the COMPLETE file
//	          X-Offset      byte offset at which this body begins; the server
//	                        must already hold exactly this many bytes
//	          X-Auth-Token  shared upload secret (optional)
//	        the body carries file bytes from X-Offset toward X-Total.
//	        response headers
//	          X-Offset      bytes the server holds after this request
//	          X-Complete    "true" once fully received AND checksum-verified
//
// The design tolerates the tunnel dropping mid-transfer: a partial PUT leaves a
// ".part" file; the sender re-queries the offset (HEAD) and resumes. Only when
// the full byte count arrives and the SHA-256 matches is the file atomically
// renamed into place.
package filetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// UploadPath is the HTTP path served by the receiver and targeted by the sender.
const UploadPath = "/upload"

// Protocol header names.
const (
	hdrFilename = "X-Filename"
	hdrTotal    = "X-Total"
	hdrSha256   = "X-Sha256"
	hdrOffset   = "X-Offset"
	hdrToken    = "X-Auth-Token"
	hdrComplete = "X-Complete"
)

// sanitizeName reduces an arbitrary client-supplied name to a safe base file
// name with no path components, rejecting traversal and empty values.
func sanitizeName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("missing X-Filename")
	}
	// Normalise Windows separators so path.Base strips them on every OS.
	raw = strings.ReplaceAll(raw, "\\", "/")
	base := path.Base(raw)
	if base == "." || base == ".." || base == "/" || base == "" || strings.ContainsRune(base, 0) {
		return "", errors.New("invalid filename")
	}
	return base, nil
}

// fileSHA256 returns the lowercase hex SHA-256 of the file at p.
func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// uniquePath returns p if it does not exist, otherwise p with a " (n)" suffix
// inserted before the extension so an arriving file never clobbers an existing
// one in the drop folder.
func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
}
