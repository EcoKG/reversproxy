package filetransfer

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Receiver writes uploaded files into a drop directory, verifying integrity and
// supporting resume. Concurrent uploads of distinct names run in parallel;
// uploads of the same name are serialised by a per-name lock.
type Receiver struct {
	dir     string
	token   string
	maxSize int64
	log     *slog.Logger

	// onComplete, if set, is invoked (in a goroutine) with the final saved path
	// after a file is fully received and verified — used by the tray to notify.
	onComplete func(savedPath string, size int64)

	mu    sync.Mutex
	locks map[string]*nameGate
}

// nameGate is a per-filename lock with a holder count so the gate map can be
// pruned once no upload is using a given name (bounded memory).
type nameGate struct {
	mu sync.Mutex
	n  int
}

// NewReceiver creates a Receiver that drops files into dir (created if missing).
// maxSize caps a single file (0 = unlimited); token (if non-empty) is required
// on every request.
func NewReceiver(dir, token string, maxSize int64, log *slog.Logger) (*Receiver, error) {
	if dir == "" {
		return nil, errors.New("filetransfer: drop directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("filetransfer: create drop dir %q: %w", dir, err)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Receiver{
		dir:     dir,
		token:   token,
		maxSize: maxSize,
		log:     log,
		locks:   map[string]*nameGate{},
	}, nil
}

// OnComplete registers a callback fired after each fully-received file.
func (r *Receiver) OnComplete(fn func(savedPath string, size int64)) { r.onComplete = fn }

// Dir returns the configured drop directory.
func (r *Receiver) Dir() string { return r.dir }

// acquire locks the per-name gate (creating it on first use) and returns it.
func (r *Receiver) acquire(name string) *nameGate {
	r.mu.Lock()
	g := r.locks[name]
	if g == nil {
		g = &nameGate{}
		r.locks[name] = g
	}
	g.n++
	r.mu.Unlock()
	g.mu.Lock()
	return g
}

// release unlocks the gate and drops it from the map when no other upload holds
// it, keeping the map bounded over a long-lived receiver.
func (r *Receiver) release(name string, g *nameGate) {
	g.mu.Unlock()
	r.mu.Lock()
	g.n--
	if g.n == 0 {
		delete(r.locks, name)
	}
	r.mu.Unlock()
}

func (r *Receiver) checkToken(req *http.Request) bool {
	if r.token == "" {
		return true
	}
	got := req.Header.Get(hdrToken)
	return subtle.ConstantTimeCompare([]byte(got), []byte(r.token)) == 1
}

func (r *Receiver) partPath(name string) string { return filepath.Join(r.dir, name+".part") }

func (r *Receiver) partSize(name string) int64 {
	fi, err := os.Stat(r.partPath(name))
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ServeHTTP implements the receive protocol (HEAD = offset query, PUT = upload).
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.checkToken(req) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := sanitizeName(req.Header.Get(hdrFilename))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Method {
	case http.MethodHead:
		w.Header().Set(hdrOffset, strconv.FormatInt(r.partSize(name), 10))
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		r.handlePut(w, req, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *Receiver) handlePut(w http.ResponseWriter, req *http.Request, name string) {
	total, err := strconv.ParseInt(req.Header.Get(hdrTotal), 10, 64)
	if err != nil || total < 0 {
		http.Error(w, "invalid X-Total", http.StatusBadRequest)
		return
	}
	if r.maxSize > 0 && total > r.maxSize {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	sumHex := strings.ToLower(req.Header.Get(hdrSha256))
	if raw, derr := hex.DecodeString(sumHex); derr != nil || len(raw) != 32 {
		http.Error(w, "invalid X-Sha256", http.StatusBadRequest)
		return
	}
	offset, err := strconv.ParseInt(req.Header.Get(hdrOffset), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "invalid X-Offset", http.StatusBadRequest)
		return
	}

	g := r.acquire(name)
	defer r.release(name, g)

	part := r.partPath(name)
	cur := r.partSize(name)
	switch {
	case offset > cur:
		// Sender is ahead of what we hold; report our real offset to resync.
		w.Header().Set(hdrOffset, strconv.FormatInt(cur, 10))
		http.Error(w, "offset mismatch", http.StatusConflict)
		return
	case offset < cur:
		// Sender is rewinding (e.g. restarting after a stale/oversized or
		// wrong-content .part). The sender is authoritative on content, so
		// truncate to the requested offset and append from there. This
		// self-heals a leftover .part that would otherwise block delivery.
		if err := os.Truncate(part, offset); err != nil {
			r.log.Error("filetransfer: truncate part", "name", name, "err", err)
			http.Error(w, "cannot reset part file", http.StatusInternalServerError)
			return
		}
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.log.Error("filetransfer: open part", "name", name, "err", err)
		http.Error(w, "cannot open part file", http.StatusInternalServerError)
		return
	}
	// Never accept more than the declared total, even if the body sends extra.
	written, copyErr := io.Copy(f, io.LimitReader(req.Body, total-cur))
	if cerr := f.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	newSize := cur + written

	if copyErr != nil {
		// Body interrupted (tunnel drop) — keep .part for resume.
		r.log.Warn("filetransfer: body interrupted", "name", name, "received", newSize, "total", total, "err", copyErr)
		r.respondProgress(w, newSize, false)
		return
	}
	if newSize < total {
		r.respondProgress(w, newSize, false)
		return
	}

	// Fully received — verify checksum before finalising.
	gotSum, err := fileSHA256(part)
	if err != nil {
		r.log.Error("filetransfer: hash part", "name", name, "err", err)
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if gotSum != sumHex {
		_ = os.Remove(part)
		r.log.Warn("filetransfer: checksum mismatch", "name", name, "want", sumHex, "got", gotSum)
		http.Error(w, "checksum mismatch", http.StatusUnprocessableEntity)
		return
	}

	final := uniquePath(filepath.Join(r.dir, name))
	if err := os.Rename(part, final); err != nil {
		r.log.Error("filetransfer: finalize rename", "name", name, "err", err)
		http.Error(w, "finalize error", http.StatusInternalServerError)
		return
	}
	r.log.Info("filetransfer: file received", "file", final, "bytes", total)
	if r.onComplete != nil {
		go r.onComplete(final, total)
	}
	r.respondProgress(w, total, true)
}

func (r *Receiver) respondProgress(w http.ResponseWriter, offset int64, complete bool) {
	w.Header().Set(hdrOffset, strconv.FormatInt(offset, 10))
	if complete {
		w.Header().Set(hdrComplete, "true")
	} else {
		w.Header().Set(hdrComplete, "false")
	}
	w.WriteHeader(http.StatusOK)
}

// StartReceiver binds an HTTP server on addr serving recv at UploadPath and
// returns the actual bound address. The server shuts down when ctx is done.
func StartReceiver(ctx context.Context, addr string, recv *Receiver) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("filetransfer: listen %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.Handle(UploadPath, recv)
	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			recv.log.Error("filetransfer: receive server stopped", "err", err)
		}
	}()
	return ln.Addr().String(), nil
}
