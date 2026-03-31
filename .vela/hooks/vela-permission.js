#!/usr/bin/env node
/**
 * ⛵ Vela PermissionRequest Hook — Auto-approves Write/Edit during delegated execution
 *
 * Fires on PermissionRequest events.
 * Auto-approves Write/Edit/NotebookEdit permissions when ALL 4 conditions are met:
 *   1. auto mode is active (state.auto === true)
 *   2. current step is 'execute'
 *   3. delegation.json exists (subagent is active)
 *   4. tool_name is in WRITE_TOOLS (Write, Edit, NotebookEdit)
 *
 * Otherwise exits silently (exit 0, empty stdout) to show the default permission dialog.
 *
 * Output format: hookSpecificOutput.decision.behavior:"allow"
 * (NOT the top-level decision:"block" pattern used by vela-stop.js)
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig } = require('./shared/pipeline');
const { WRITE_TOOLS } = require('./shared/constants');

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

  // K003: call once at top of main(), reuse below
  const state = findActivePipeline(velaDir);
  if (!state) process.exit(0);

  // ─── 4-condition AND gate ───
  // 1. Auto mode active
  if (state.auto !== true) process.exit(0);

  // 2. Current step is execute
  if (state.current_step !== 'execute') process.exit(0);

  // 3. Delegation signal exists (subagent is active)
  const delegationPath = path.join(velaDir, 'state', 'delegation.json');
  if (!fs.existsSync(delegationPath)) process.exit(0);

  // 4. Tool is a write tool (Write, Edit, NotebookEdit)
  if (!WRITE_TOOLS.has(input.tool_name)) process.exit(0);

  // All conditions met — auto-approve the permission
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'PermissionRequest',
      decision: {
        behavior: 'allow'
      }
    }
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
