(() => {
  const POLL_MS = 2000;
  const $ = (id) => document.getElementById(id);

  function fmtBytes(n) {
    if (!Number.isFinite(n)) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
  }

  function fmtUptime(ms) {
    if (ms < 0) ms = 0;
    const s = Math.floor(ms / 1000);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = s % 60;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m ${sec}s`;
    return `${sec}s`;
  }

  function fmtTime(iso) {
    try {
      const d = new Date(iso);
      return d.toLocaleTimeString();
    } catch { return iso; }
  }

  async function fetchJSON(path) {
    const r = await fetch(path, { headers: { Accept: 'application/json' } });
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.json();
  }

  function renderClients(clients) {
    const tbody = document.querySelector('#clients-table tbody');
    if (!clients || clients.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty">연결된 클라이언트가 없습니다.</td></tr>';
      return;
    }
    const now = Date.now();
    tbody.innerHTML = clients.map((c) => {
      const connectedAt = c.connected_at ? new Date(c.connected_at).getTime() : now;
      return `<tr>
        <td><strong>${escapeHtml(c.name || '—')}</strong></td>
        <td><code>${escapeHtml(c.addr || '—')}</code></td>
        <td>${fmtTime(c.connected_at)}</td>
        <td>${fmtUptime(now - connectedAt)}</td>
      </tr>`;
    }).join('');
  }

  function renderTunnels(tunnels) {
    const tbody = document.querySelector('#tunnels-table tbody');
    if (!tunnels || tunnels.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty">활성 터널이 없습니다.</td></tr>';
      return;
    }
    tbody.innerHTML = tunnels.map((t) => {
      const exposure = t.type === 'tcp'
        ? `<code>${escapeHtml(t.public_addr || '—')}</code>`
        : `<code>${escapeHtml(t.hostname || '—')}</code>`;
      return `<tr>
        <td><span class="badge ${escapeHtml(t.type)}">${escapeHtml(t.type)}</span></td>
        <td>${exposure}</td>
        <td><code>${escapeHtml(t.local_addr || '—')}</code></td>
        <td><code>${escapeHtml(t.client_id || '—')}</code></td>
      </tr>`;
    }).join('');
  }

  function renderStats(stats) {
    $('s-total').textContent = stats.total_connections ?? 0;
    $('s-active').textContent = stats.active_connections ?? 0;
    $('s-in').textContent = fmtBytes(stats.bytes_in ?? 0);
    $('s-out').textContent = fmtBytes(stats.bytes_out ?? 0);

    const tunnels = stats.tunnels || {};
    const ids = Object.keys(tunnels);
    const tbody = document.querySelector('#tunnel-stats-table tbody');
    if (ids.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty">트래픽 통계 없음.</td></tr>';
    } else {
      tbody.innerHTML = ids.map((id) => {
        const s = tunnels[id];
        return `<tr>
          <td><code>${escapeHtml(id)}</code></td>
          <td>${s.connection_count ?? 0}</td>
          <td>${fmtBytes(s.bytes_in ?? 0)}</td>
          <td>${fmtBytes(s.bytes_out ?? 0)}</td>
        </tr>`;
      }).join('');
    }
  }

  function setStatus(ok, msg) {
    const dot = $('status-dot');
    dot.classList.remove('ok', 'err');
    dot.classList.add(ok ? 'ok' : 'err');
    $('status-text').textContent = msg;
  }

  async function refresh() {
    try {
      const [clients, tunnels, stats] = await Promise.all([
        fetchJSON('/api/clients'),
        fetchJSON('/api/tunnels'),
        fetchJSON('/api/stats'),
      ]);
      renderClients(clients.clients || []);
      renderTunnels(tunnels.tunnels || []);
      renderStats(stats || {});
      $('s-clients').textContent = (clients.clients || []).length;
      $('s-tunnels').textContent = (tunnels.tunnels || []).length;
      $('last-update').textContent = `updated ${new Date().toLocaleTimeString()}`;
      setStatus(true, 'connected');
    } catch (err) {
      setStatus(false, `error: ${err.message}`);
    }
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[c]);
  }

  $('interval').textContent = POLL_MS / 1000;
  refresh();
  setInterval(refresh, POLL_MS);
})();
