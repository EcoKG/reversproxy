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

const fs = require('fs');
const path = require('path');
const { findActivePipeline, getCurrentMode, readConfig } = require('./shared/pipeline');
const {
  CODE_EXTENSIONS,
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

  // Cache pipeline state — won't change within a single hook invocation
  const pipelineState = findActivePipeline(velaDir);

  // ─── GATE 1: Bash Blocking ───
  // Vela uses its own CLI. Bash is blocked unless it's a Vela CLI command,
  // a safe read-only command, or a git/gh command during an active pipeline.
  if (tool_name === 'Bash') {
    const cmd = (tool_input.command || '').trim();

    // Allow Vela CLI commands
    if (cmd.startsWith('node ') && cmd.includes('.vela/cli/')) {
      process.exit(0);
    }
    if (cmd.startsWith('python') && cmd.includes('.vela/cli/')) {
      process.exit(0);
    }

    // Allow safe read-only bash commands in all modes
    if (SAFE_BASH_READ.test(cmd)) {
      process.exit(0);
    }

    // Allow git/gh commands during active pipeline
    // Gate Guard (VG-07, VG-08) handles step-based restrictions
    // Permission Deny rules handle dangerous commands (--force, --hard, --no-verify)
    if (/^\s*(git|gh)\s/.test(cmd)) {
      const velaState = pipelineState;
      if (velaState) {
        process.exit(0);
      }
    }

    // Check for write patterns in bash
    const hasWritePattern = BASH_WRITE_PATTERNS.some(p => p.test(cmd));

    if (hasWritePattern && currentMode === 'read') {
      process.stderr.write(
        `⛵ [Vela] ✦ BLOCKED [VK-01]: Bash write command in read-only mode.\n` +
        `  Mode: ${currentMode}\n` +
        `  Command: ${cmd.substring(0, 100)}\n` +
        `  Recovery: Use Vela CLI tools instead: .vela/cli/vela-write`
      );
      process.exit(2);
    }

    // Block all other bash commands
    process.stderr.write(
      `⛵ [Vela] ✦ BLOCKED [VK-02]: Bash is restricted in Vela sandbox.\n` +
      `  Command: ${cmd.substring(0, 100)}\n` +
      `  Recovery: Use Vela CLI tools (.vela/cli/vela-read, .vela/cli/vela-write)\n` +
      `  or Claude Code's built-in Read/Write/Edit/Glob/Grep tools.`
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
          `⛵ [Vela] ✦ BLOCKED [VK-03]: Cannot directly modify pipeline-state.json.\n` +
          `  Recovery: Use engine CLI: node .vela/cli/vela-engine.js transition`
        );
        process.exit(2);
      }
      process.exit(0);
    }

    process.stderr.write(
      `⛵ [Vela] ✦ BLOCKED [VK-04]: Write operation in read-only mode.\n` +
      `  Tool: ${tool_name}\n` +
      `  Target: ${targetFile}\n` +
      `  Recovery: Advance pipeline to write-enabled step: node .vela/cli/vela-engine.js transition`
    );
    process.exit(2);
  }

  // ─── GATE 2.5: PM Source Code Access Prohibition ───
  // PM(본체)은 소스 코드에 절대 직접 접근하지 않는다.
  // 모든 소스 코드 읽기/쓰기는 반드시 Subagent 또는 Teammate에 위임한다.
  // 예외: trivial 파이프라인 (PM 직접 수행), .vela/ 내부 파일, 설정 파일
  const ALL_CODE_TOOLS = new Set([...WRITE_TOOLS, 'Read', 'Glob', 'Grep']);
  if (ALL_CODE_TOOLS.has(tool_name)) {
    const state = pipelineState;
    if (state && state.pipeline_type !== 'trivial') {
      const targetFile = tool_input.file_path || tool_input.path || tool_input.pattern || '';

      // .vela/ 내부 파일은 항상 허용 (오케스트레이션에 필수)
      if (targetFile.includes('.vela/') || targetFile.includes('.vela\\')) {
        // pass through to next gate
      }
      // CLAUDE.md, package.json 등 프로젝트 루트 설정 파일 허용
      else if (/^(CLAUDE\.md|package\.json|tsconfig\.json|\.gitignore|README\.md)$/i.test(path.basename(targetFile))) {
        // pass through
      }
      // Glob/Grep: Read 계열 도구는 PM의 소스 코드 속독(速讀)을 허용한다.
      // Glob/Grep 자체는 Read 계열이므로 항상 통과.
      else if ((tool_name === 'Glob' || tool_name === 'Grep') && !targetFile.includes('.vela/')) {
        // pass through — Glob/Grep은 읽기 전용, 위임 불필요
      }
      // Read/Write/Edit: 소스 코드 파일
      // Read는 PM 속독 허용, Write/Edit만 delegation.json 필요
      else if (targetFile && !targetFile.includes('.vela/')) {
        const ext = path.extname(targetFile).toLowerCase();
        const isSourceCode = CODE_EXTENSIONS.has(ext);
        const inSkipPath = SKIP_PATHS.some(sp => targetFile.includes(sp));

        if (isSourceCode && !inSkipPath && WRITE_TOOLS.has(tool_name)) {
          const delegationPath = path.join(velaDir, 'state', 'delegation.json');
          if (!fs.existsSync(delegationPath)) {
            process.stderr.write(
              `⛵ [Vela] ✦ BLOCKED [VK-07]: PM은 소스 코드를 직접 수정할 수 없습니다.\n` +
              `  Tool: ${tool_name} | File: ${targetFile}\n` +
              `  이 작업은 반드시 Subagent 또는 Teammate에 위임해야 합니다.\n` +
              `  Recovery: Agent 도구로 에이전트를 소환하세요.\n` +
              `  - 코드 구현 → Subagent (model: "sonnet")\n` +
              `  - 다중 파일 수정 → Teammate (model: "sonnet", isolation: "worktree")`
            );
            process.exit(2);
          }
        }
      }
    }
  }

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
        `⛵ [Vela] ✦ BLOCKED [VK-05]: Cannot write to sensitive file.\n` +
        `  File: ${targetFile}\n` +
        `  Recovery: Use .env.example or .env.template instead of ${SENSITIVE_FILES.join(', ')}`
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
          `⛵ [Vela] ✦ BLOCKED [VK-06]: Potential secret/credential detected in write.\n` +
          `  Tool: ${tool_name}\n` +
          `  Pattern: ${pattern.source.substring(0, 40)}...\n` +
          `  Recovery: Remove secret from code. Use environment variables instead.`
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
