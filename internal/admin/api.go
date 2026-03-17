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
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/EcoKG/reversproxy/internal/control"
	"github.com/EcoKG/reversproxy/internal/stats"
	"github.com/EcoKG/reversproxy/internal/tunnel"
)

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
	reg      *control.ClientRegistry
	mgr      *tunnel.Manager
	statsReg *stats.Registry
	global   *stats.ServerStats
	log      *slog.Logger
	httpSrv  *http.Server
}

// New creates a new admin Server. statsReg and global may be nil; in that
// case the stats endpoint returns zeroed counters.
func New(
	reg *control.ClientRegistry,
	mgr *tunnel.Manager,
	statsReg *stats.Registry,
	global *stats.ServerStats,
	log *slog.Logger,
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
		log:      log,
	}
}

// Start starts the admin HTTP server on addr in a background goroutine.
// The server is shut down when ctx is cancelled.
func (s *Server) Start(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("admin: listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/clients", s.handleClients)
	mux.HandleFunc("/api/tunnels", s.handleTunnels)
	mux.HandleFunc("/api/stats", s.handleStats)

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
