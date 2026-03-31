#!/usr/bin/env node
/**
 * ⛵ Vela StopFailure Hook — Preserves pipeline state on API errors
 *
 * Fires when a session stops due to error (rate_limit, server_error, etc).
 * - Writes state snapshot to artifact directory for post-mortem
 * - Logs to trace.jsonl
 * - Writes NO stdout (StopFailure output is FULLY IGNORED by Claude Code)
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig } = require('./shared/pipeline');

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0);
  }

  const cwd = input.cwd || process.cwd();
  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);
  if (!config) process.exit(0);

  // K003: call once at top, reuse below
  const state = findActivePipeline(velaDir);
  if (!state) process.exit(0);

  const { error, error_details, last_assistant_message } = input;

  // ─── Write state snapshot to artifact directory ───
  if (state._artifactDir) {
    const snapshotPath = path.join(state._artifactDir, `stop-failure-${Date.now()}.json`);
    try {
      fs.writeFileSync(snapshotPath, JSON.stringify({
        error: error || 'unknown',
        error_details: error_details || null,
        last_assistant_message: (last_assistant_message || '').substring(0, 500),
        pipeline_snapshot: {
          status: state.status || '?',
          current_step: state.current_step || '?',
          completed_steps: state.completed_steps || [],
          pipeline_type: state.pipeline_type || '?',
          request: (state.request || '').substring(0, 200)
        },
        timestamp: Date.now()
      }, null, 2));
    } catch (e) {}
  }

  // ─── Log to trace.jsonl ───
  if (state._artifactDir) {
    const tracePath = path.join(state._artifactDir, 'trace.jsonl');
    try {
      fs.appendFileSync(tracePath, JSON.stringify({
        action: 'stop_failure',
        error: error || 'unknown',
        step: state.current_step || '?',
        timestamp: Date.now()
      }) + '\n');
    } catch (e) {}
  }

  // NOTE: No stdout — StopFailure output is FULLY IGNORED by Claude Code
  process.exit(0);
}

main().catch(() => process.exit(0));
