#!/usr/bin/env node
/**
 * ⛵ Vela TeammateIdle Hook — Detects idle teammates and emits alert
 *
 * Fires when a teammate in an Agent Team goes idle.
 * - Logs idle event to trace.jsonl
 * - Emits systemMessage for operator visibility
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

  const { teammate_name, team_name } = input;

  // ─── Log to trace.jsonl ───
  if (state._artifactDir) {
    const tracePath = path.join(state._artifactDir, 'trace.jsonl');
    try {
      fs.appendFileSync(tracePath, JSON.stringify({
        action: 'teammate_idle',
        teammate_name: teammate_name || 'unknown',
        team_name: team_name || 'unknown',
        step: state.current_step || '?',
        timestamp: Date.now()
      }) + '\n');
    } catch (e) {}
  }

  // ─── Emit systemMessage for observability ───
  const name = teammate_name || 'unknown';
  process.stdout.write(JSON.stringify({
    systemMessage: `⛵ [Vela] Teammate '${name}' idle — 작업 할당을 확인하세요.`
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
