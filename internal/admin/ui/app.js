(() => {
  const POLL_MS = 2000;
  const MAX_EVENTS = 100;
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
      return new Date(iso).toLocaleTimeString();
    } catch { return iso; }
  }

  async function fetchJSON(path) {
    const r = await fetch(path, { headers: { Accept: 'application/json' } });
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.json();
  }

  async function postJSON(path) {
    const r = await fetch(path, { method: 'POST' });
    if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`);
    return r.json().catch(() => ({}));
  }

  async function deleteRequest(path) {
    const r = await fetch(path, { method: 'DELETE' });
    if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`);
    return r.json().catch(() => ({}));
  }

  function escapeHtml(s) {
    return String(s ?? '').replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[c]);
  }

  function shortFp(fp) {
    if (!fp) return '—';
    return fp.length > 24 ? fp.slice(0, 24) + '…' : fp;
  }

  // ---------------------------------------------------------------- pending
  function renderPending(pending) {
    const card = $('pending-card');
    const tbody = document.querySelector('#pending-table tbody');
    const count = pending?.length || 0;
    $('pending-count').textContent = count;
    if (count === 0) {
      card.hidden = true;
      tbody.innerHTML = '';
      return;
    }
    card.hidden = false;
    tbody.innerHTML = pending.map((p) => `
      <tr>
        <td><strong>${escapeHtml(p.client_name)}</strong></td>
        <td><code>${escapeHtml(p.addr)}</code></td>
        <td title="${escapeHtml(p.fingerprint)}"><code>${escapeHtml(shortFp(p.fingerprint))}</code></td>
        <td>${fmtTime(p.requested_at)}</td>
        <td class="actions">
          <button class="btn approve" data-name="${escapeHtml(p.client_name)}">승인</button>
          <button class="btn reject"  data-name="${escapeHtml(p.client_name)}">거부</button>
        </td>
      </tr>`).join('');

    tbody.querySelectorAll('.btn.approve').forEach((b) => {
      b.addEventListener('click', () => decide(b.dataset.name, 'approve'));
    });
    tbody.querySelectorAll('.btn.reject').forEach((b) => {
      b.addEventListener('click', () => decide(b.dataset.name, 'reject'));
    });
  }

  async function decide(name, action) {
    try {
      await postJSON(`/api/decide?name=${encodeURIComponent(name)}&action=${action}`);
      addEvent({ type: `local.${action}`, name, time: new Date().toISOString() });
      refresh();
    } catch (err) {
      addEvent({ type: 'local.error', detail: err.message, time: new Date().toISOString() });
    }
  }

  // ---------------------------------------------------------------- clients
  function renderClients(clients) {
    const tbody = document.querySelector('#clients-table tbody');
    if (!clients || clients.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty">연결된 클라이언트가 없습니다.</td></tr>';
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
        <td class="actions">
          <button class="btn reconnect" data-name="${escapeHtml(c.name || '')}">재연결</button>
        </td>
      </tr>`;
    }).join('');

    tbody.querySelectorAll('.btn.reconnect').forEach((b) => {
      b.addEventListener('click', async () => {
        const name = b.dataset.name;
        if (!name) return;
        try {
          await postJSON(`/api/reconnect?name=${encodeURIComponent(name)}`);
          addEvent({ type: 'local.reconnect_requested', name, time: new Date().toISOString() });
        } catch (err) {
          addEvent({ type: 'local.error', detail: err.message, time: new Date().toISOString() });
        }
      });
    });
  }

  // ---------------------------------------------------------------- tunnels
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

  // ---------------------------------------------------------------- stats
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

  // ---------------------------------------------------------------- known
  function renderKnown(hosts) {
    const tbody = document.querySelector('#known-table tbody');
    if (!hosts || hosts.length === 0) {
      tbody.innerHTML = '<tr><td colspan="3" class="empty">등록된 호스트가 없습니다.</td></tr>';
      return;
    }
    tbody.innerHTML = hosts.map((h) => `
      <tr>
        <td><strong>${escapeHtml(h.name)}</strong></td>
        <td title="${escapeHtml(h.fingerprint)}"><code>${escapeHtml(shortFp(h.fingerprint))}</code></td>
        <td class="actions">
          <button class="btn revoke" data-name="${escapeHtml(h.name)}">취소</button>
        </td>
      </tr>`).join('');

    tbody.querySelectorAll('.btn.revoke').forEach((b) => {
      b.addEventListener('click', async () => {
        if (!confirm(`'${b.dataset.name}' 신뢰 해제?`)) return;
        try {
          await deleteRequest(`/api/known-hosts?name=${encodeURIComponent(b.dataset.name)}`);
          addEvent({ type: 'local.revoke', name: b.dataset.name, time: new Date().toISOString() });
          refresh();
        } catch (err) {
          addEvent({ type: 'local.error', detail: err.message, time: new Date().toISOString() });
        }
      });
    });
  }

  // ---------------------------------------------------------------- events
  function addEvent(ev) {
    const list = $('events-list');
    if (list.querySelector('.empty')) list.innerHTML = '';
    const li = document.createElement('li');
    li.className = `evt evt-${(ev.type || 'unknown').replace(/\./g, '-')}`;
    const t = ev.time ? fmtTime(ev.time) : fmtTime(new Date().toISOString());
    const detail = ev.detail ? ` <span class="evt-detail">${escapeHtml(ev.detail)}</span>` : '';
    const name = ev.name ? ` <strong>${escapeHtml(ev.name)}</strong>` : '';
    const addr = ev.addr ? ` <code>${escapeHtml(ev.addr)}</code>` : '';
    li.innerHTML = `<span class="evt-time">${t}</span> <span class="evt-type">${escapeHtml(ev.type || '?')}</span>${name}${addr}${detail}`;
    list.prepend(li);
    while (list.children.length > MAX_EVENTS) list.removeChild(list.lastChild);
  }

  function connectEvents() {
    let es;
    try {
      es = new EventSource('/api/events');
    } catch {
      $('evt-status').textContent = 'unavailable';
      return;
    }
    es.addEventListener('open', () => {
      $('evt-status').textContent = 'connected';
      $('evt-status').className = 'badge ok';
    });
    es.addEventListener('error', () => {
      $('evt-status').textContent = 'reconnecting…';
      $('evt-status').className = 'badge';
      // Browser auto-reconnects EventSource, no manual handling needed.
    });
    es.addEventListener('message', (e) => {
      try {
        addEvent(JSON.parse(e.data));
      } catch { /* ignore malformed */ }
    });
  }

  // ---------------------------------------------------------------- main
  function setStatus(ok, msg) {
    const dot = $('status-dot');
    dot.classList.remove('ok', 'err');
    dot.classList.add(ok ? 'ok' : 'err');
    $('status-text').textContent = msg;
  }

  async function refresh() {
    try {
      const [clients, tunnels, stats, pending, known] = await Promise.all([
        fetchJSON('/api/clients'),
        fetchJSON('/api/tunnels'),
        fetchJSON('/api/stats'),
        fetchJSON('/api/pending').catch(() => ({ pending: [] })),
        fetchJSON('/api/known-hosts').catch(() => ({ hosts: [] })),
      ]);
      renderClients(clients.clients || []);
      renderTunnels(tunnels.tunnels || []);
      renderStats(stats || {});
      renderPending(pending.pending || []);
      renderKnown(known.hosts || []);
      $('s-clients').textContent = (clients.clients || []).length;
      $('s-tunnels').textContent = (tunnels.tunnels || []).length;
      $('last-update').textContent = `updated ${new Date().toLocaleTimeString()}`;
      setStatus(true, 'connected');
    } catch (err) {
      setStatus(false, `error: ${err.message}`);
    }
  }

  $('interval').textContent = POLL_MS / 1000;
  refresh();
  setInterval(refresh, POLL_MS);
  connectEvents();
})();
