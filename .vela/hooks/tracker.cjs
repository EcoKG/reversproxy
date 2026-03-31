#!/usr/bin/env node
/**
 * Vela Tracker — PostToolUse Hook
 *
 * Fires on every PostToolUse event from Claude Code.
 * Logs tool_use, agent_dispatch, and build_test_signal entries to trace.jsonl
 * inside the active pipeline's artifact directory.
 *
 * Fail-open: exits 0 on all paths. PostToolUse hooks must never block.
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline } = require('./shared/pipeline.cjs');

// ─── Build/Test Pattern Detection ───

const BUILD_PATTERNS = [
  /\b(npm|npx|yarn|pnpm)\s+(run\s+)?build\b/,
  /\btsc\b/,
  /\bgo\s+build\b/,
  /\bcargo\s+build\b/,
  /\bmake\b/,
  /\bgradlew?\s+build\b/,
  /\bmvn\s+(compile|package)\b/,
  /\bdotnet\s+build\b/,
];

const TEST_PATTERNS = [
  /\b(npm|npx|yarn|pnpm)\s+(run\s+)?test\b/,
  /\b(jest|vitest|mocha|ava)\b/,
  /\bpytest\b/,
  /\bgo\s+test\b/,
  /\bcargo\s+test\b/,
  /\bgradlew?\s+test\b/,
  /\bmvn\s+test\b/,
  /\bdotnet\s+test\b/,
];

const FAIL_INDICATORS = [
  /(?<!0\s)(error|fail|FAIL|ERROR)/i,
  /exit\s+code\s+[1-9]/,
  /FAILED/,
  /npm\s+ERR!/,
];

const PASS_INDICATORS = [
  /\bpass(ed)?\b/i,
  /\b0\s+(errors?|failures?)\b/i,
  /\ball\s+tests?\s+pass/i,
  /✓|✔|PASS/,
];

// ─── Agent dispatch tool names ───

const AGENT_TOOLS = new Set(['Agent', 'Task', 'dispatch_agent']);

// ─── Trace helpers ───

function appendTraceEntry(artifactDir, entry) {
  if (!artifactDir) return;
  try {
    const tracePath = path.join(artifactDir, 'trace.jsonl');
    fs.appendFileSync(tracePath, JSON.stringify(entry) + '\n');
  } catch (_e) {
    // fail-open: swallow write errors
  }
}

// ─── Main ───

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (_e) {
    process.exit(0);
  }

  if (!input || typeof input !== 'object') process.exit(0);

  const { tool_name, tool_input, tool_output, session_id, cwd } = input;
  if (!tool_name || !cwd) process.exit(0);

  // Locate active pipeline
  const velaDir = path.join(cwd, '.vela');
  let state;
  try {
    state = findActivePipeline(velaDir);
  } catch (_e) {
    process.exit(0);
  }

  if (!state || !state._artifactDir) process.exit(0);

  const artifactDir = state._artifactDir;

  // ─── 1. Log tool_use entry ───
  appendTraceEntry(artifactDir, {
    action: 'tool_use',
    tool: tool_name,
    step: state.current_step || null,
    timestamp: Date.now(),
  });

  // ─── 2. Detect agent dispatches ───
  if (AGENT_TOOLS.has(tool_name)) {
    appendTraceEntry(artifactDir, {
      action: 'agent_dispatch',
      tool: tool_name,
      description: (tool_input && tool_input.description) || '',
      step: state.current_step || null,
      timestamp: Date.now(),
    });
  }

  // ─── 3. Detect build/test signals from Bash commands ───
  if (tool_name === 'Bash' && tool_input) {
    const cmd = (tool_input.command || '').toString();
    const result = (tool_output || '').toString();

    for (const pattern of BUILD_PATTERNS) {
      if (pattern.test(cmd)) {
        const isFail = FAIL_INDICATORS.some((fi) => fi.test(result));
        const isPass = PASS_INDICATORS.some((pi) => pi.test(result));
        appendTraceEntry(artifactDir, {
          action: 'build_test_signal',
          signal_type: 'build',
          result: isFail ? 'fail' : isPass ? 'pass' : 'unknown',
          command: cmd.substring(0, 200),
          step: state.current_step || null,
          timestamp: Date.now(),
        });
        break;
      }
    }

    for (const pattern of TEST_PATTERNS) {
      if (pattern.test(cmd)) {
        const isFail = FAIL_INDICATORS.some((fi) => fi.test(result));
        const isPass = PASS_INDICATORS.some((pi) => pi.test(result));
        appendTraceEntry(artifactDir, {
          action: 'build_test_signal',
          signal_type: 'test',
          result: isFail ? 'fail' : isPass ? 'pass' : 'unknown',
          command: cmd.substring(0, 200),
          step: state.current_step || null,
          timestamp: Date.now(),
        });
        break;
      }
    }
  }

  process.exit(0);
}

main().catch(() => process.exit(0));
