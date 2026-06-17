//go:build windows

package main

import (
	"fmt"
	"time"

	"github.com/lxn/walk"

	"github.com/EcoKG/reversproxy/internal/app"
)

// clientRow is a value snapshot of one connected client (no pointers into the
// live server state, so the UI thread never races server goroutines).
type clientRow struct {
	ID      string
	Name    string
	Addr    string
	Since   string
	Uptime  string
	Tunnels int
}

type clientModel struct {
	walk.TableModelBase
	items []clientRow
}

func (m *clientModel) RowCount() int { return len(m.items) }

func (m *clientModel) Value(row, col int) interface{} {
	r := m.items[row]
	switch col {
	case 0:
		return r.Name
	case 1:
		return r.Addr
	case 2:
		return r.Since
	case 3:
		return r.Uptime
	case 4:
		return r.Tunnels
	}
	return ""
}

func (m *clientModel) idAt(row int) string {
	if row < 0 || row >= len(m.items) {
		return ""
	}
	return m.items[row].ID
}

func (m *clientModel) setItems(items []clientRow) {
	m.items = items
	m.PublishRowsReset()
}

// tunnelRow is a value snapshot of one active tunnel.
type tunnelRow struct {
	Type  string
	Route string
	Local string
	Bytes string
}

type tunnelModel struct {
	walk.TableModelBase
	items []tunnelRow
}

func (m *tunnelModel) RowCount() int { return len(m.items) }

func (m *tunnelModel) Value(row, col int) interface{} {
	r := m.items[row]
	switch col {
	case 0:
		return r.Type
	case 1:
		return r.Route
	case 2:
		return r.Local
	case 3:
		return r.Bytes
	}
	return ""
}

func (m *tunnelModel) setItems(items []tunnelRow) {
	m.items = items
	m.PublishRowsReset()
}

// snapshotClients builds a race-free snapshot of connected clients, counting
// the tunnels owned by each.
func snapshotClients(a *app.ServerApp) []clientRow {
	clients := a.Registry().List()
	count := map[string]int{}
	for _, t := range a.Manager().ListTunnels() {
		count[t.ClientID]++
	}
	for _, h := range a.Manager().ListHTTPTunnels() {
		count[h.ClientID]++
	}
	rows := make([]clientRow, 0, len(clients))
	for _, c := range clients {
		rows = append(rows, clientRow{
			ID:      c.ID,
			Name:    c.Name,
			Addr:    c.Addr,
			Since:   c.RegisteredAt.Format("15:04:05"),
			Uptime:  time.Since(c.RegisteredAt).Round(time.Second).String(),
			Tunnels: count[c.ID],
		})
	}
	return rows
}

// snapshotTunnels flattens TCP + HTTP/HTTPS tunnels into display rows. Per-tunnel
// byte counters are not wired into the relay path yet, so bytes render as "—"
// rather than a fake 0.
func snapshotTunnels(a *app.ServerApp) []tunnelRow {
	var rows []tunnelRow
	for _, t := range a.Manager().ListTunnels() {
		rows = append(rows, tunnelRow{
			Type:  "tcp",
			Route: fmt.Sprintf("공인 :%d", t.PublicPort),
			Local: fmt.Sprintf("%s:%d", t.LocalHost, t.LocalPort),
			Bytes: "—",
		})
	}
	for _, h := range a.Manager().ListHTTPTunnels() {
		typ := "http"
		if h.IsTLS {
			typ = "https"
		}
		rows = append(rows, tunnelRow{
			Type:  typ,
			Route: h.Hostname,
			Local: fmt.Sprintf("%s:%d", h.LocalHost, h.LocalPort),
			Bytes: "—",
		})
	}
	return rows
}
