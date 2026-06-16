package filetransfer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SendOptions tunes a send operation.
type SendOptions struct {
	// Token is the shared upload secret (must match the receiver's, if set).
	Token string
	// MaxRetries is the number of retries after the first attempt (default 5).
	MaxRetries int
	// RetryWait is the base linear backoff between attempts (default 1s).
	RetryWait time.Duration
}

// SendFile uploads the file at filePath to endpoint (e.g. "http://127.0.0.1:8089"),
// resuming from the receiver's current offset and verifying integrity via
// SHA-256. Transient failures (including a tunnel drop) are retried with linear
// backoff; each retry re-queries the offset and resumes.
func SendFile(ctx context.Context, endpoint, filePath string, opts SendOptions, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 5
	}
	if opts.RetryWait == 0 {
		opts.RetryWait = time.Second
	}

	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("filetransfer: stat %q: %w", filePath, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("filetransfer: %q is a directory (zip it before sending)", filePath)
	}
	total := fi.Size()
	name := filepath.Base(filePath)

	sum, err := fileSHA256(filePath)
	if err != nil {
		return fmt.Errorf("filetransfer: hash %q: %w", filePath, err)
	}

	uploadURL := strings.TrimRight(endpoint, "/") + UploadPath
	client := &http.Client{} // no overall timeout; ctx governs cancellation

	// A 409 (offset mismatch) is a benign "resume from the corrected offset"
	// signal, not a failure, so it must not spend the retry budget — but bound
	// it so a persistent contention race cannot loop forever.
	maxConflicts := 2*(opts.MaxRetries+1) + 4
	var lastErr error
	attempt, conflicts := 0, 0
	for attempt <= opts.MaxRetries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * opts.RetryWait):
			}
		}

		offset, err := remoteOffset(ctx, client, uploadURL, name, opts.Token)
		if err != nil {
			lastErr = err
			log.Warn("filetransfer: offset query failed", "attempt", attempt, "err", err)
			attempt++
			continue
		}
		if offset > total {
			offset = 0 // stale/oversized part — restart from 0 (receiver truncates)
		}

		done, conflict, err := putFrom(ctx, client, uploadURL, filePath, name, sum, total, offset, opts.Token)
		switch {
		case err != nil:
			lastErr = err
			log.Warn("filetransfer: upload failed", "attempt", attempt, "offset", offset, "err", err)
			attempt++
		case done:
			log.Info("filetransfer: sent", "file", name, "bytes", total)
			return nil
		case conflict:
			conflicts++
			if conflicts > maxConflicts {
				return fmt.Errorf("filetransfer: send %q failed: offset conflicts did not converge", name)
			}
			// re-query and resume without consuming the retry budget
		default:
			// Partial progress (body cut server-side); resume on next attempt.
			lastErr = fmt.Errorf("incomplete upload")
			attempt++
		}
	}
	return fmt.Errorf("filetransfer: send %q failed after %d attempts: %w", name, opts.MaxRetries+1, lastErr)
}

// remoteOffset asks the receiver how many bytes it already holds for name.
func remoteOffset(ctx context.Context, client *http.Client, url, name, token string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set(hdrFilename, name)
	if token != "" {
		req.Header.Set(hdrToken, token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("offset query: status %d", resp.StatusCode)
	}
	off, _ := strconv.ParseInt(resp.Header.Get(hdrOffset), 10, 64)
	if off < 0 {
		off = 0
	}
	return off, nil
}

// putFrom uploads the file's bytes from offset to EOF. It returns done=true when
// the receiver confirms full receipt + checksum match, or conflict=true when the
// receiver reports an offset mismatch (HTTP 409) and the caller should re-query.
func putFrom(ctx context.Context, client *http.Client, url, filePath, name, sum string, total, offset int64, token string) (done, conflict bool, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false, false, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, io.LimitReader(f, total-offset))
	if err != nil {
		return false, false, err
	}
	req.Header.Set(hdrFilename, name)
	req.Header.Set(hdrTotal, strconv.FormatInt(total, 10))
	req.Header.Set(hdrOffset, strconv.FormatInt(offset, 10))
	req.Header.Set(hdrSha256, sum)
	if token != "" {
		req.Header.Set(hdrToken, token)
	}
	req.ContentLength = total - offset

	resp, err := client.Do(req)
	if err != nil {
		return false, false, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Header.Get(hdrComplete) == "true", false, nil
	case http.StatusConflict:
		return false, true, nil // offset mismatch; caller re-queries and resumes
	default:
		return false, false, fmt.Errorf("upload: status %d", resp.StatusCode)
	}
}
