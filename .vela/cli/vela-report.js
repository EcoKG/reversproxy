#!/usr/bin/env node
/**
 * ⛵ Vela Report — Generate HTML pipeline dashboard
 *
 * Usage: node .vela/cli/vela-report.js [--html output.html]
 */

const fs = require('fs');
const path = require('path');

const CWD = process.cwd();
const VELA_DIR = path.join(CWD, '.vela');
const ARTIFACTS_DIR = path.join(VELA_DIR, 'artifacts');
const args = process.argv.slice(2);
const htmlOutput = args.indexOf('--html') >= 0 ? args[args.indexOf('--html') + 1] : null;

// Collect all pipelines
const pipelines = [];
if (fs.existsSync(ARTIFACTS_DIR)) {
  const allDirs = fs.readdirSync(ARTIFACTS_DIR).sort().reverse();

  // Flat structure: {date}_{id}_{slug}/
  for (const dir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}_/.test(d))) {
    const dirPath = path.join(ARTIFACTS_DIR, dir);
    try { if (!fs.statSync(dirPath).isDirectory()) continue; } catch { continue; }
    const sp = path.join(dirPath, 'pipeline-state.json');
    if (!fs.existsSync(sp)) continue;
    try {
      const state = JSON.parse(fs.readFileSync(sp, 'utf-8'));
      const artifacts = fs.readdirSync(dirPath).filter(f => f.endsWith('.md') || f.endsWith('.json') || f.endsWith('.patch'));
      const date = dir.split('_')[0];
      pipelines.push({
        date, slug: dir, status: state.status, type: state.pipeline_type,
        request: state.request, step: state.current_step,
        completed: (state.completed_steps || []).length, total: (state.steps || []).length,
        created: state.created_at, updated: state.updated_at,
        artifacts, git: state.git || null
      });
    } catch (e) {}
  }

  // Backward compat: nested {date}/{slug}/
  for (const dd of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d))) {
    const dp = path.join(ARTIFACTS_DIR, dd);
    let slugs;
    try { slugs = fs.readdirSync(dp).filter(d => fs.statSync(path.join(dp, d)).isDirectory()); } catch { continue; }
    for (const s of slugs) {
      const sp = path.join(dp, s, 'pipeline-state.json');
      if (!fs.existsSync(sp)) continue;
      try {
        const state = JSON.parse(fs.readFileSync(sp, 'utf-8'));
        const artifacts = fs.readdirSync(path.join(dp, s)).filter(f => f.endsWith('.md') || f.endsWith('.json') || f.endsWith('.patch'));
        pipelines.push({
          date: dd, slug: s, status: state.status, type: state.pipeline_type,
          request: state.request, step: state.current_step,
          completed: (state.completed_steps || []).length, total: (state.steps || []).length,
          created: state.created_at, updated: state.updated_at,
          artifacts, git: state.git || null
        });
      } catch (e) {}
    }
  }
}

if (htmlOutput) {
  const statusColor = s => s === 'completed' ? '#22c55e' : s === 'active' ? '#3b82f6' : '#ef4444';
  const rows = pipelines.map(p => `
    <tr>
      <td>${p.date}</td>
      <td><span style="color:${statusColor(p.status)}">${p.status}</span></td>
      <td>${p.type}</td>
      <td>${(p.request || '').substring(0, 40)}</td>
      <td>${p.step}</td>
      <td>${p.completed}/${p.total}</td>
      <td>${p.artifacts.length}</td>
      <td>${p.git?.pipeline_branch || '-'}</td>
    </tr>`).join('\n');

  const html = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>⛵ Vela Dashboard</title>
<style>
body{font-family:system-ui;background:#0f172a;color:#e2e8f0;padding:2rem;max-width:1200px;margin:0 auto}
h1{text-align:center;font-size:2rem}
table{width:100%;border-collapse:collapse;margin-top:2rem}
th{background:#1e293b;padding:0.75rem;text-align:left;border-bottom:2px solid #334155}
td{padding:0.75rem;border-bottom:1px solid #1e293b}
tr:hover{background:#1e293b}
.stats{display:flex;gap:2rem;justify-content:center;margin-top:1rem}
.stat{text-align:center;padding:1rem 2rem;background:#1e293b;border-radius:8px}
.stat-num{font-size:2rem;font-weight:bold;color:#38bdf8}
.stat-label{font-size:0.85rem;color:#94a3b8}
</style></head><body>
<h1>⛵ Vela Dashboard</h1>
<div class="stats">
  <div class="stat"><div class="stat-num">${pipelines.length}</div><div class="stat-label">Total Pipelines</div></div>
  <div class="stat"><div class="stat-num">${pipelines.filter(p=>p.status==='completed').length}</div><div class="stat-label">Completed</div></div>
  <div class="stat"><div class="stat-num">${pipelines.filter(p=>p.status==='active').length}</div><div class="stat-label">Active</div></div>
</div>
<table>
<tr><th>Date</th><th>Status</th><th>Type</th><th>Task</th><th>Step</th><th>Progress</th><th>Artifacts</th><th>Branch</th></tr>
${rows}
</table>
</body></html>`;

  fs.writeFileSync(htmlOutput, html);
  console.log(JSON.stringify({ ok: true, command: 'report', output: htmlOutput, pipelines: pipelines.length }));
} else {
  console.log(JSON.stringify({ ok: true, command: 'report', pipelines: pipelines }, null, 2));
}
