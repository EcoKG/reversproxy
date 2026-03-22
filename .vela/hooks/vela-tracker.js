#!/usr/bin/env node
/**
 * Vela Tracker — PostToolUse Hook
 *
 * Monitors all tool usage after execution to maintain system awareness.
 *
 * Responsibilities:
 * 1. Track tool usage count and context pressure
 * 2. Detect build/test pass/fail signals
 * 3. Update TreeNode cache on file reads
 * 4. Log actions to trace file
 * 5. Track agent dispatches during execute step
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig, getSessionStatePath } = require('./shared/pipeline');
const { CODE_EXTENSIONS, SKIP_PATHS } = require('./shared/constants');

const BUILD_PATTERNS = [
  { pattern: /\b(npm|npx|yarn|pnpm)\s+(run\s+)?build\b/, type: 'build' },
  { pattern: /\btsc\b/, type: 'build' },
  { pattern: /\bgo\s+build\b/, type: 'build' },
  { pattern: /\bcargo\s+build\b/, type: 'build' },
  { pattern: /\bmake\b/, type: 'build' },
  { pattern: /\bgradlew?\s+build\b/, type: 'build' },
  { pattern: /\bmvn\s+(compile|package)\b/, type: 'build' },
  { pattern: /\bdotnet\s+build\b/, type: 'build' }
];

const TEST_PATTERNS = [
  { pattern: /\b(npm|npx|yarn|pnpm)\s+(run\s+)?test\b/, type: 'test' },
  { pattern: /\b(jest|vitest|mocha|ava)\b/, type: 'test' },
  { pattern: /\bpytest\b/, type: 'test' },
  { pattern: /\bgo\s+test\b/, type: 'test' },
  { pattern: /\bcargo\s+test\b/, type: 'test' },
  { pattern: /\bgradlew?\s+test\b/, type: 'test' },
  { pattern: /\bmvn\s+test\b/, type: 'test' },
  { pattern: /\bdotnet\s+test\b/, type: 'test' }
];

const FAIL_INDICATORS = [
  /(?<!0\s)(error|fail|FAIL|ERROR)/i,
  /exit\s+code\s+[1-9]/,
  /FAILED/,
  /npm\s+ERR!/
];

const PASS_INDICATORS = [
  /\bpass(ed)?\b/i,
  /\b0\s+(errors?|failures?)\b/i,
  /\ball\s+tests?\s+pass/i,
  /✓|✔|PASS/
];

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0);
  }

  const { tool_name, tool_input, tool_output, session_id, cwd } = input;
  if (!tool_name || !cwd) process.exit(0);

  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);
  if (!config) process.exit(0);

  const output = [];

  // ─── Context Pressure Tracking ───
  const sessionStatePath = getSessionStatePath(session_id, cwd);
  let sessionState = { tool_count: 0 };
  try {
    if (fs.existsSync(sessionStatePath)) {
      sessionState = JSON.parse(fs.readFileSync(sessionStatePath, 'utf-8'));
    }
  } catch (e) {}

  sessionState.tool_count = (sessionState.tool_count || 0) + 1;

  if (sessionState.tool_count === 100) {
    output.push('🔭 [Vela] Context pressure: MEDIUM (100 tool calls). Consider completing current step.');
  } else if (sessionState.tool_count === 150) {
    output.push('🔭 [Vela] Context pressure: HIGH (150 tool calls). Finalize current work soon.');
  } else if (sessionState.tool_count >= 180 && sessionState.tool_count % 10 === 0) {
    output.push('🔭 [Vela] Context pressure: CRITICAL. Complete pipeline step immediately.');
  }

  try {
    fs.writeFileSync(sessionStatePath, JSON.stringify(sessionState));
  } catch (e) {}

  // ─── Build/Test Signal Detection ───
  if (tool_name === 'Bash' && tool_output) {
    const cmd = tool_input.command || '';
    const result = tool_output || '';

    for (const bp of [...BUILD_PATTERNS, ...TEST_PATTERNS]) {
      if (bp.pattern.test(cmd)) {
        const isFail = FAIL_INDICATORS.some(fi => fi.test(result));
        const isPass = PASS_INDICATORS.some(pi => pi.test(result));
        const signal = {
          type: bp.type,
          result: isFail ? 'fail' : (isPass ? 'pass' : 'unknown'),
          command: cmd.substring(0, 100),
          timestamp: Date.now()
        };

        appendSignal(velaDir, signal);

        if (isFail) {
          output.push(`🔭 [Vela] ${bp.type.toUpperCase()} FAILED — fix before proceeding.`);
        }
        break;
      }
    }
  }

  // ─── Read Counter (for read-throttle warning in gate-guard) ───
  const pipelineForCounter = findActivePipeline(velaDir);
  if (pipelineForCounter && (tool_name === 'Read' || tool_name === 'Glob' || tool_name === 'Grep')) {
    const counterPath = path.join(velaDir, 'state', 'reads-since-transition.json');
    let counter = { step: pipelineForCounter.current_step, count: 0 };
    try {
      if (fs.existsSync(counterPath)) {
        counter = JSON.parse(fs.readFileSync(counterPath, 'utf-8'));
        if (counter.step !== pipelineForCounter.current_step) {
          counter = { step: pipelineForCounter.current_step, count: 0 };
        }
      }
    } catch (e) {}
    counter.count++;
    try {
      const stateDir = path.join(velaDir, 'state');
      if (!fs.existsSync(stateDir)) fs.mkdirSync(stateDir, { recursive: true });
      fs.writeFileSync(counterPath, JSON.stringify(counter));
    } catch (e) {}
  }

  // ─── TreeNode Cache Update ───
  // When a file is read, record its path in the TreeNode cache
  if (tool_name === 'Read' && config.cache && config.cache.treenode_enabled) {
    const filePath = tool_input.file_path || '';
    if (filePath && !SKIP_PATHS.some(sp => filePath.includes(sp))) {
      appendTreeNodeEntry(velaDir, filePath, session_id);
    }
  }

  if (tool_name === 'Glob') {
    // Glob results would be in tool_output, but we track the search pattern
    appendTraceEntry(velaDir, {
      action: 'glob',
      pattern: tool_input.pattern || '',
      path: tool_input.path || cwd,
      timestamp: Date.now()
    });
  }

  // ─── Agent Dispatch Detection ───
  const state = findActivePipeline(velaDir);
  if (state && tool_name === 'Agent') {
    appendTraceEntry(velaDir, {
      action: 'agent_dispatch',
      description: tool_input.description || '',
      step: state.current_step,
      timestamp: Date.now()
    });
  }

  // ─── Trace Logging ───
  if (state) {
    appendTraceEntry(velaDir, {
      action: 'tool_use',
      tool: tool_name,
      step: state.current_step,
      timestamp: Date.now()
    });
  }

  if (output.length > 0) {
    process.stdout.write(output.join('\n'));
  }

  process.exit(0);
}

function appendSignal(velaDir, signal) {
  const signalsPath = path.join(velaDir, 'tracker-signals.json');
  let signals = [];
  try {
    if (fs.existsSync(signalsPath)) {
      signals = JSON.parse(fs.readFileSync(signalsPath, 'utf-8'));
    }
  } catch (e) {
    signals = [];
  }

  signals.push(signal);

  // Cap at 20 entries
  if (signals.length > 20) {
    signals = signals.slice(-20);
  }

  try {
    const tmpPath = signalsPath + '.tmp';
    fs.writeFileSync(tmpPath, JSON.stringify(signals, null, 2));
    fs.renameSync(tmpPath, signalsPath);
  } catch (e) {}
}

function appendTreeNodeEntry(velaDir, filePath, sessionId) {
  // Append to a simple JSONL file for the TreeNode cache to ingest
  const cachePath = path.join(velaDir, 'cache', 'pending-paths.jsonl');
  try {
    const cacheDir = path.dirname(cachePath);
    if (!fs.existsSync(cacheDir)) {
      fs.mkdirSync(cacheDir, { recursive: true });
    }
    fs.appendFileSync(cachePath, JSON.stringify({
      path: filePath,
      session_id: sessionId,
      timestamp: Date.now()
    }) + '\n');
  } catch (e) {}
}

function appendTraceEntry(velaDir, entry) {
  const state = findActivePipeline(velaDir);
  if (!state || !state._artifactDir) return;

  const tracePath = path.join(state._artifactDir, 'trace.jsonl');
  try {
    fs.appendFileSync(tracePath, JSON.stringify(entry) + '\n');
  } catch (e) {}
}

main().catch(() => process.exit(0));
