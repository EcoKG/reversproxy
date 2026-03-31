#!/usr/bin/env node
/**
 * ⛵ Vela Cost Report — Pipeline cost/token summary
 *
 * Reads trace.jsonl from the active pipeline's artifact directory
 * and produces a cost summary.
 *
 * Usage: node .vela/cli/vela-cost.js
 */

const fs = require('fs');
const path = require('path');

const CWD = process.cwd();
const VELA_DIR = path.join(CWD, '.vela');
const ARTIFACTS_DIR = path.join(VELA_DIR, 'artifacts');

// Find active or most recent pipeline
let artifactDir = null;
if (fs.existsSync(ARTIFACTS_DIR)) {
  const dateDirs = fs.readdirSync(ARTIFACTS_DIR)
    .filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d)).sort().reverse();
  for (const dd of dateDirs) {
    const dp = path.join(ARTIFACTS_DIR, dd);
    const slugs = fs.readdirSync(dp).filter(d => {
      try { return fs.statSync(path.join(dp, d)).isDirectory(); } catch { return false; }
    }).sort().reverse();
    for (const s of slugs) {
      const sp = path.join(dp, s, 'pipeline-state.json');
      if (fs.existsSync(sp)) {
        artifactDir = path.join(dp, s);
        break;
      }
    }
    if (artifactDir) break;
  }
}

if (!artifactDir) {
  console.log(JSON.stringify({ ok: false, error: 'No pipeline found.' }));
  process.exit(1);
}

const state = JSON.parse(fs.readFileSync(path.join(artifactDir, 'pipeline-state.json'), 'utf-8'));
const tracePath = path.join(artifactDir, 'trace.jsonl');

let toolCalls = 0;
let agentDispatches = 0;
const stepCounts = {};

if (fs.existsSync(tracePath)) {
  const lines = fs.readFileSync(tracePath, 'utf-8').trim().split('\n');
  for (const line of lines) {
    try {
      const entry = JSON.parse(line);
      if (entry.action === 'tool_use') {
        toolCalls++;
        const step = entry.step || 'unknown';
        stepCounts[step] = (stepCounts[step] || 0) + 1;
      }
      if (entry.action === 'agent_dispatch') agentDispatches++;
    } catch (e) {}
  }
}

const created = new Date(state.created_at);
const updated = new Date(state.updated_at);
const durationMin = Math.round((updated - created) / 60000);

const artifacts = fs.readdirSync(artifactDir).filter(f => !f.startsWith('.')).length;

console.log(JSON.stringify({
  ok: true,
  command: 'cost',
  pipeline: {
    type: state.pipeline_type,
    status: state.status,
    request: state.request,
    steps_completed: (state.completed_steps || []).length,
    steps_total: (state.steps || []).length
  },
  metrics: {
    tool_calls: toolCalls,
    agent_dispatches: agentDispatches,
    artifacts_produced: artifacts,
    duration_minutes: durationMin,
    step_breakdown: stepCounts
  }
}, null, 2));
