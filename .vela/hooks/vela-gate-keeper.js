#!/usr/bin/env node
/**
 * Vela Gate Keeper (수문장) — PreToolUse Hook
 *
 * The gatekeeper controls ALL read/write access within the Vela sandbox.
 * Every tool invocation passes through this gate before execution.
 *
 * Responsibilities:
 * 1. Enforce current mode (read/write/readwrite) restrictions
 * 2. Block Bash tool usage (Vela uses its own CLI)
 * 3. Detect and block secret/credential leaks
 * 4. Protect sensitive files
 *
 * Exit codes:
 *   0 — Action permitted
 *   2 — Action blocked (hard block)
 *
 * stdout — Non-blocking warnings
 * stderr — Block reason (when exit 2)
 */

const path = require('path');
const { findActivePipeline, getCurrentMode, readConfig } = require('./shared/pipeline');
const {
  WRITE_TOOLS,
  READ_TOOLS,
  SENSITIVE_FILES,
  SECRET_PATTERNS,
  SAFE_BASH_READ,
  BASH_WRITE_PATTERNS,
  SKIP_PATHS
} = require('./shared/constants');

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0); // Can't parse input, let it through
  }

  const { tool_name, tool_input, session_id, cwd } = input;
  if (!tool_name || !cwd) process.exit(0);

  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);

  // If Vela is not initialized in this project, pass through
  if (!config || !config.sandbox || !config.sandbox.enabled) {
    process.exit(0);
  }

  const currentMode = getCurrentMode(cwd);

  // ─── GATE 1: Bash Blocking ───
  // Vela uses its own CLI. Bash is blocked unless it's a Vela CLI command
  // or a safe read-only command in read/readwrite mode.
  if (tool_name === 'Bash') {
    const cmd = (tool_input.command || '').trim();

    // Allow Vela CLI commands
    if (cmd.startsWith('node ') && cmd.includes('.vela/cli/')) {
      process.exit(0);
    }
    if (cmd.startsWith('python') && cmd.includes('.vela/cli/')) {
      process.exit(0);
    }

    // In read/readwrite mode, allow safe read-only bash commands
    if ((currentMode === 'read' || currentMode === 'readwrite') && SAFE_BASH_READ.test(cmd)) {
      process.exit(0);
    }

    // Check for write patterns in bash
    const hasWritePattern = BASH_WRITE_PATTERNS.some(p => p.test(cmd));

    if (hasWritePattern && currentMode === 'read') {
      process.stderr.write(
        `⛵ [Vela] ✦ BLOCKED: Bash write command in read-only mode.\n` +
        `  Mode: ${currentMode}\n` +
        `  Command: ${cmd.substring(0, 100)}\n` +
        `  Use Vela CLI tools instead: .vela/cli/vela-write`
      );
      process.exit(2);
    }

    // Block all other bash commands
    process.stderr.write(
      `⛵ [Vela] ✦ BLOCKED: Bash is restricted in Vela sandbox.\n` +
      `  Command: ${cmd.substring(0, 100)}\n` +
      `  Use Vela CLI tools instead (.vela/cli/vela-read, .vela/cli/vela-write)\n` +
      `  Or use Claude Code's built-in Read/Write/Edit/Glob/Grep tools.`
    );
    process.exit(2);
  }

  // ─── GATE 2: Mode Enforcement ───
  // Read-only mode: block write tools
  if (currentMode === 'read' && WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';

    // Allow writes to .vela/ internal files — EXCEPT pipeline-state.json
    if (targetFile.includes('.vela/')) {
      if (path.basename(targetFile) === 'pipeline-state.json') {
        process.stderr.write(
          `⛵ [Vela] ✦ BLOCKED: Cannot directly modify pipeline-state.json.\n` +
          `  Pipeline state is managed exclusively by the Vela engine.`
        );
        process.exit(2);
      }
      process.exit(0);
    }

    process.stderr.write(
      `⛵ [Vela] ✦ BLOCKED: Write operation in read-only mode.\n` +
      `  Tool: ${tool_name}\n` +
      `  Target: ${targetFile}\n` +
      `  Current step requires read-only mode.\n` +
      `  Advance the pipeline to a write-enabled step first.`
    );
    process.exit(2);
  }

  // Write-only mode: allow reads (needed for context) but don't block writes
  // No restrictions needed for write-only beyond what gate guard handles

  // ─── GATE 3: Sensitive File Protection ───
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';
    const fileName = path.basename(targetFile);

    if (SENSITIVE_FILES.includes(fileName)) {
      // Allow template files (e.g., .env.example)
      if (fileName.includes('.example') || fileName.includes('.template') || fileName.includes('.sample')) {
        process.exit(0);
      }

      process.stderr.write(
        `⛵ [Vela] ✦ BLOCKED: Cannot write to sensitive file.\n` +
        `  File: ${targetFile}\n` +
        `  Protected files: ${SENSITIVE_FILES.join(', ')}\n` +
        `  Use .env.example or .env.template instead.`
      );
      process.exit(2);
    }
  }

  // ─── GATE 4: Secret Detection ───
  if (WRITE_TOOLS.has(tool_name)) {
    const content = tool_input.content || tool_input.new_string || '';
    for (const pattern of SECRET_PATTERNS) {
      if (pattern.test(content)) {
        process.stderr.write(
          `⛵ [Vela] ✦ BLOCKED: Potential secret/credential detected in write.\n` +
          `  Tool: ${tool_name}\n` +
          `  Pattern matched: ${pattern.source.substring(0, 40)}...\n` +
          `  Never embed secrets in source code. Use environment variables.`
        );
        process.exit(2);
      }
    }
  }

  // ─── GATE 5: Skip Path Warning ───
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';
    const inSkipPath = SKIP_PATHS.some(sp => targetFile.includes(sp));
    if (inSkipPath && !targetFile.includes('.vela/')) {
      process.stdout.write(
        `⛵ [Vela] ⚠ WARNING: Writing to a typically excluded path.\n` +
        `  File: ${targetFile}\n` +
        `  This path is normally skipped during analysis.`
      );
    }
  }

  // All gates passed
  process.exit(0);
}

main().catch(() => process.exit(0));
