// Package admin provides a lightweight HTTP JSON API for inspecting the
// live state of the reverse proxy server.
//
// Endpoints:
//
//	GET /api/clients — list all connected clients
//	GET /api/tunnels — list all active tunnels
//	GET /api/stats   — server-wide traffic statistics
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/stats"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

//go:embed ui/index.html ui/app.js ui/style.css
var uiFS embed.FS

// -----------------------------------------------------------------------
// Wire types
// -----------------------------------------------------------------------

// ClientInfo is the JSON representation of a connected client.
type ClientInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Addr        string    `json:"addr"`
	ConnectedAt time.Time `json:"connected_at"`
}

// TunnelInfo is the JSON representation of a single active tunnel.
type TunnelInfo struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id"`
	Type       string `json:"type"` // "tcp", "http", "https"
	LocalAddr  string `json:"local_addr"`
	PublicAddr string `json:"public_addr,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
}

// StatsInfo is the JSON representation of the aggregate traffic statistics.
type StatsInfo struct {
	TotalConnections  int64                        `json:"total_connections"`
	ActiveConnections int64                        `json:"active_connections"`
	BytesIn           int64                        `json:"bytes_in"`
	BytesOut          int64                        `json:"bytes_out"`
	Tunnels           map[string]stats.TunnelSnapshot `json:"tunnels,omitempty"`
}

// -----------------------------------------------------------------------
// Server
// -----------------------------------------------------------------------

// Server wraps an http.Server that exposes the admin API.
type Server struct {
	reg        *control.ClientRegistry
	mgr        *tunnel.Manager
	statsReg   *stats.Registry
	global     *stats.ServerStats
	token      string
	log        *slog.Logger
	httpSrv    *http.Server
	approver   *control.Approver
	knownHosts *control.KnownHosts
	events     *EventBus
}

// WithApproval attaches the TOFU approval queue and known_hosts store so that
// /api/pending and the approve/reject endpoints become functional.
func (s *Server) WithApproval(a *control.Approver, k *control.KnownHosts) *Server {
	s.approver = a
	s.knownHosts = k
	return s
}

// WithEvents attaches the event bus that backs the /api/events SSE stream.
func (s *Server) WithEvents(b *EventBus) *Server {
	s.events = b
	return s
}

// New creates a new admin Server. statsReg and global may be nil; in that
// case the stats endpoint returns zeroed counters. token is a Bearer token
// required for authentication; empty means no auth.
func New(
	reg *control.ClientRegistry,
	mgr *tunnel.Manager,
	statsReg *stats.Registry,
	global *stats.ServerStats,
	log *slog.Logger,
	token string,
) *Server {
	if global == nil {
		global = stats.Global
	}
	if statsReg == nil {
		statsReg = stats.NewRegistry()
	}
	return &Server{
		reg:      reg,
		mgr:      mgr,
		statsReg: statsReg,
		global:   global,
		token:    token,
		log:      log,
	}
}

// authMiddleware returns a handler that checks for a valid Bearer token
// before calling next. If no token is configured, it passes through.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Start starts the admin HTTP server on addr in a background goroutine.
// The server is shut down when ctx is cancelled.
func (s *Server) Start(ctx context.Context, addr string) error {
	// Default to localhost-only when no host is specified.
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("admin: listen %s: %w", addr, err)
	}

	return s.StartWithListener(ctx, ln)
}

// StartWithListener starts the admin HTTP server using an already-bound
// net.Listener. This eliminates the TOCTOU race that would otherwise occur
// between obtaining a free port and binding to it.
// The server is shut down when ctx is cancelled.
func (s *Server) StartWithListener(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/clients", s.authMiddleware(s.handleClients))
	mux.HandleFunc("/api/tunnels", s.authMiddleware(s.handleTunnels))
	mux.HandleFunc("/api/stats", s.authMiddleware(s.handleStats))
	mux.HandleFunc("/api/pending", s.authMiddleware(s.handlePending))
	mux.HandleFunc("/api/known-hosts", s.authMiddleware(s.handleKnownHosts))
	mux.HandleFunc("/api/decide", s.authMiddleware(s.handleDecide))
	mux.HandleFunc("/api/reconnect", s.authMiddleware(s.handleReconnect))
	mux.HandleFunc("/api/events", s.authMiddleware(s.handleEvents))

	// Dashboard UI (HTML/JS/CSS embedded at build time).
	staticFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return fmt.Errorf("admin: ui sub fs: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/", s.handleIndex)

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.log.Info("admin API listener started", "addr", ln.Addr().String())

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("admin server error", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
	}()

	return nil
}

// -----------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------

// handleIndex serves the dashboard at "/". Any other unknown path returns 404.
// The dashboard polls the JSON API endpoints from the browser, so the HTML
// itself does not need to be authenticated; the JSON endpoints still are
// (when a token is configured).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui not embedded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// handleClients responds with the list of connected clients.
//
// Response shape:
//
//	{ "clients": [ { "id": "...", "name": "...", "addr": "...", "connected_at": "..." } ] }
func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	raw := s.reg.List()
	clients := make([]ClientInfo, 0, len(raw))
	for _, c := range raw {
		clients = append(clients, ClientInfo{
			ID:          c.ID,
			Name:        c.Name,
			Addr:        c.Addr,
			ConnectedAt: c.RegisteredAt,
		})
	}

	writeJSON(w, map[string]any{"clients": clients})
}

// handleTunnels responds with the list of active tunnels (TCP + HTTP + HTTPS).
//
// Response shape:
//
//	{ "tunnels": [ { "id": "...", "client_id": "...", "type": "tcp|http|https",
//	                 "local_addr": "...", "public_addr": "...", "hostname": "..." } ] }
func (s *Server) handleTunnels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tunnels := s.mgr.ListTunnels()
	infos := make([]TunnelInfo, 0, len(tunnels))
	for _, te := range tunnels {
		infos = append(infos, TunnelInfo{
			ID:         te.ID,
			ClientID:   te.ClientID,
			Type:       "tcp",
			LocalAddr:  fmt.Sprintf("%s:%d", te.LocalHost, te.LocalPort),
			PublicAddr: fmt.Sprintf(":%d", te.PublicPort),
		})
	}

	httpTunnels := s.mgr.ListHTTPTunnels()
	for _, ht := range httpTunnels {
		tunnelType := "http"
		if ht.IsTLS {
			tunnelType = "https"
		}
		infos = append(infos, TunnelInfo{
			ID:        ht.ID,
			ClientID:  ht.ClientID,
			Type:      tunnelType,
			LocalAddr: fmt.Sprintf("%s:%d", ht.LocalHost, ht.LocalPort),
			Hostname:  ht.Hostname,
		})
	}

	writeJSON(w, map[string]any{"tunnels": infos})
}

// handleStats responds with aggregate traffic statistics.
//
// Response shape:
//
//	{
//	  "total_connections":  123,
//	  "active_connections": 4,
//	  "bytes_in":           1024,
//	  "bytes_out":          2048,
//	  "tunnels": { "<tunnelID>": { "connection_count": 10, "bytes_in": 512, "bytes_out": 1024 } }
//	}
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := StatsInfo{
		TotalConnections:  s.global.TotalConnections.Load(),
		ActiveConnections: s.global.ActiveConnections.Load(),
		BytesIn:           s.global.BytesIn.Load(),
		BytesOut:          s.global.BytesOut.Load(),
		Tunnels:           s.statsReg.Snapshot(),
	}

	writeJSON(w, info)
}

// handlePending returns the list of clients currently waiting for operator
// TOFU approval.
func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.approver == nil {
		writeJSON(w, map[string]any{"pending": []any{}})
		return
	}
	writeJSON(w, map[string]any{"pending": s.approver.List()})
}

// handleKnownHosts returns the list of approved client fingerprints.
func (s *Server) handleKnownHosts(w http.ResponseWriter, r *http.Request) {
	if s.knownHosts == nil {
		writeJSON(w, map[string]any{"hosts": []any{}})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"hosts": s.knownHosts.List()})
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		if err := s.knownHosts.Remove(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDecide approves or rejects a pending TOFU request.
//
// Request: POST /api/decide?name=<client>&action=approve|reject
func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.approver == nil {
		http.Error(w, "approval disabled", http.StatusServiceUnavailable)
		return
	}
	name := r.URL.Query().Get("name")
	action := r.URL.Query().Get("action")
	if name == "" || (action != "approve" && action != "reject") {
		http.Error(w, "name and action=approve|reject required", http.StatusBadRequest)
		return
	}
	if !s.approver.Decide(name, action == "approve") {
		http.Error(w, "no pending request for that name", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleReconnect forces all currently-active connections matching name to
// drop, causing the dial loop to re-dial that client target.
//
// Request: POST /api/reconnect?name=<client>
func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	n := s.reg.DisconnectByName(name)
	writeJSON(w, map[string]any{"ok": true, "disconnected": n})
}

// handleEvents streams server events as Server-Sent Events. Each event is a
// single JSON object on a "data:" line. The connection stays open until the
// client disconnects or the server context is cancelled.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.events == nil {
		http.Error(w, "events disabled", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cleanup := s.events.Subscribe()
	defer cleanup()

	// Initial comment to flush headers immediately.
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
