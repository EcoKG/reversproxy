#!/usr/bin/env node
/**
 * ⛵ Vela PostToolUseFailure Hook — Logs tool failures and tracks consecutive failure count
 *
 * Fires when a tool invocation fails.
 * - Logs failure to trace.jsonl for pipeline traceability
 * - Tracks consecutive failures in failure-counter.json (atomic write)
 * - At 3 consecutive failures, emits warning to change approach
 * - Resets counter on interrupt (user-caused, not a real failure)
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig } = require('./shared/pipeline');

const CONSECUTIVE_THRESHOLD = 3;

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

  const { tool_name, error, is_interrupt } = input;

  // ─── Log failure to trace.jsonl ───
  if (state._artifactDir) {
    const tracePath = path.join(state._artifactDir, 'trace.jsonl');
    try {
      fs.appendFileSync(tracePath, JSON.stringify({
        action: 'tool_failure',
        tool: tool_name || 'unknown',
        error: (error || '').substring(0, 200),
        step: state.current_step || '?',
        timestamp: Date.now()
      }) + '\n');
    } catch (e) {}
  }

  // ─── Consecutive failure counter (atomic write) ───
  const stateDir = path.join(velaDir, 'state');
  if (!fs.existsSync(stateDir)) {
    try { fs.mkdirSync(stateDir, { recursive: true }); } catch (e) {}
  }

  const counterPath = path.join(stateDir, 'failure-counter.json');
  let counter = { count: 0, last_tool: null, last_timestamp: null };
  try {
    if (fs.existsSync(counterPath)) {
      counter = JSON.parse(fs.readFileSync(counterPath, 'utf-8'));
    }
  } catch (e) {
    counter = { count: 0, last_tool: null, last_timestamp: null };
  }

  if (is_interrupt) {
    // Interrupt is user-caused — reset counter
    counter = { count: 0, last_tool: null, last_timestamp: null };
  } else {
    counter.count = (counter.count || 0) + 1;
    counter.last_tool = tool_name || 'unknown';
    counter.last_timestamp = Date.now();
  }

  // Atomic write: .tmp + rename
  try {
    const tmpPath = counterPath + '.tmp';
    fs.writeFileSync(tmpPath, JSON.stringify(counter, null, 2));
    fs.renameSync(tmpPath, counterPath);
  } catch (e) {}

  // ─── Threshold warning ───
  if (counter.count >= CONSECUTIVE_THRESHOLD) {
    process.stdout.write(JSON.stringify({
      additionalContext: '⚠️ [Vela] 연속 도구 실패 3회 — 접근 방식을 변경하세요.'
    }));

    // Reset counter after emitting warning
    try {
      const tmpPath = counterPath + '.tmp';
      fs.writeFileSync(tmpPath, JSON.stringify({ count: 0, last_tool: null, last_timestamp: null }, null, 2));
      fs.renameSync(tmpPath, counterPath);
    } catch (e) {}
  }

  process.exit(0);
}

main().catch(() => process.exit(0));
