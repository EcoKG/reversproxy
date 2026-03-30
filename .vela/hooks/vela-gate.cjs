#!/usr/bin/env node
/**
 * Vela Gate (통합 게이트) — PreToolUse Hook
 *
 * Unified hook merging gate-keeper (sandbox enforcement) and gate-guard
 * (pipeline compliance). Runs VK-* gates first, then VG-* guards — matching
 * the original two-hook execution order.
 *
 * I/O deduplication: shared pipeline helpers are each called exactly once
 * at the top (config, active pipeline, pipeline def, step def).
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
const pipeline = require('./shared/pipeline.cjs');
const {
  CODE_EXTENSIONS,
  WRITE_TOOLS,
  READ_TOOLS,
  SENSITIVE_FILES,
  SECRET_PATTERNS,
  SAFE_BASH_READ,
  BASH_WRITE_PATTERNS,
  SKIP_PATHS
} = require('./shared/constants.cjs');

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

  // ══════════════════════════════════════════════════════════
  // SHARED I/O — each helper called exactly once
  // ══════════════════════════════════════════════════════════
  const config = pipeline.readConfig(cwd);
  const sandboxEnabled = !!(config && config.sandbox && config.sandbox.enabled);
  const guardEnabled = !!(config && config.gate_guard && config.gate_guard.enabled);

  // Neither feature enabled → pass through
  if (!sandboxEnabled && !guardEnabled) process.exit(0);

  const state = pipeline.findActivePipeline(velaDir);
  const pipelineDef = pipeline.readPipelineDefinition(cwd);
  const stepDef = pipeline.getCurrentStepDef(pipelineDef, state);
  const currentMode = (stepDef && stepDef.mode) || 'read';
  const currentStep = state ? state.current_step : null;
  const artifactDir = state ? state._artifactDir : null;

  // delegation.json existence — read once, used by VK-07 and VG-12
  const delegationPath = path.join(velaDir, 'state', 'delegation.json');
  const isDelegated = fs.existsSync(delegationPath);
  const targetFile = tool_input.file_path || tool_input.path || '';

  // ══════════════════════════════════════════════════════════
  // UNIFIED: pipeline-state.json protection
  // Merges VK-03, VG-05 GUARD 2, VG-05 GUARD 5
  // ══════════════════════════════════════════════════════════
  if (WRITE_TOOLS.has(tool_name)) {
    if (path.basename(targetFile) === 'pipeline-state.json') {
      process.stderr.write(`[VK-03] pipeline-state.json is immutable. Run: node .vela/cli/vela-engine.js transition`);
      process.exit(2);
    }
  }

  // ══════════════════════════════════════════════════════════
  // GATE-KEEPER GATES (VK-*) — sandbox mode enforcement
  // ══════════════════════════════════════════════════════════
  if (sandboxEnabled) {

    // ─── VK-01 / VK-02: Bash Blocking ───
    if (tool_name === 'Bash') {
      const cmd = (tool_input.command || '').trim();

      const isVelaCli =
        (cmd.startsWith('node ') && cmd.includes('.vela/cli/')) ||
        (cmd.startsWith('python') && cmd.includes('.vela/cli/'));
      const isSafeRead = SAFE_BASH_READ.test(cmd);
      const isGitWithPipeline = /^\s*(git|gh)\s/.test(cmd) && state;

      if (!isVelaCli && !isSafeRead && !isGitWithPipeline) {
        const hasWritePattern = BASH_WRITE_PATTERNS.some(p => p.test(cmd));
        if (hasWritePattern && currentMode === 'read') {
          process.stderr.write(`[VK-01] Bash write blocked in read mode. Run: node .vela/cli/vela-engine.js transition`);
          process.exit(2);
        }

        process.stderr.write(`[VK-02] Bash restricted in sandbox. Use .vela/cli/ tools or built-in Read/Write/Edit/Glob/Grep.`);
        process.exit(2);
      }
      // Allowed bash (Vela CLI, safe read, git/gh with pipeline) falls through to guards
    }

    // ─── VK-04: Mode Enforcement — block writes in read-only mode ───
    if (currentMode === 'read' && WRITE_TOOLS.has(tool_name)) {
      // Allow .vela/ internal writes (pipeline-state.json already handled above).
      // Don't exit — fall through so guard section can still check VG-11 etc.
      if (!targetFile.includes('.vela/')) {
        process.stderr.write(`[VK-04] Write blocked in read mode. Run: node .vela/cli/vela-engine.js transition`);
        process.exit(2);
      }
    }

    // ─── VK-07: PM Source Code Access Prohibition ───
    const ALL_CODE_TOOLS = new Set([...WRITE_TOOLS, 'Read', 'Glob', 'Grep']);
    if (ALL_CODE_TOOLS.has(tool_name) && state && state.pipeline_type !== 'trivial') {
      const codeTarget = tool_input.pattern || targetFile;

      if (!codeTarget.includes('.vela/') && !codeTarget.includes('.vela\\')) {
        if (!/^(CLAUDE\.md|package\.json|tsconfig\.json|\.gitignore|README\.md)$/i.test(path.basename(codeTarget))) {

          // Glob/Grep: source exploration requires delegation
          if ((tool_name === 'Glob' || tool_name === 'Grep') && !isDelegated) {
            process.stderr.write(`[VK-07] PM cannot explore source code directly. Delegate to a Subagent.`);
            process.exit(2);
          }

          // Read/Write/Edit: source code files require delegation
          if (codeTarget && tool_name !== 'Glob' && tool_name !== 'Grep') {
            const ext = path.extname(codeTarget).toLowerCase();
            const isSourceCode = CODE_EXTENSIONS.has(ext);
            const inSkipPath = SKIP_PATHS.some(sp => codeTarget.includes(sp));

            if (isSourceCode && !inSkipPath && !isDelegated) {
              process.stderr.write(`[VK-07] PM cannot access source code directly. Delegate to a Subagent.`);
              process.exit(2);
            }
          }
        }
      }
    }

    // ─── VK-05: Sensitive File Protection ───
    if (WRITE_TOOLS.has(tool_name)) {
      const fileName = path.basename(targetFile);

      if (SENSITIVE_FILES.includes(fileName)) {
        if (!fileName.includes('.example') && !fileName.includes('.template') && !fileName.includes('.sample')) {
          process.stderr.write(`[VK-05] Sensitive file protected. Use .env.example or .env.template instead.`);
          process.exit(2);
        }
      }
    }

    // ─── VK-06: Secret Detection ───
    if (WRITE_TOOLS.has(tool_name)) {
      const content = tool_input.content || tool_input.new_string || '';
      for (const pattern of SECRET_PATTERNS) {
        if (pattern.test(content)) {
          process.stderr.write(`[VK-06] Secret/credential detected in write content. Use environment variables instead.`);
          process.exit(2);
        }
      }
    }

    // ─── Skip Path Warning (non-blocking) ───
    if (WRITE_TOOLS.has(tool_name)) {
      const inSkipPath = SKIP_PATHS.some(sp => targetFile.includes(sp));
      if (inSkipPath && !targetFile.includes('.vela/')) {
        process.stdout.write(`[VK] WARNING: Writing to a typically excluded path.`);
      }
    }
  }

  // ══════════════════════════════════════════════════════════
  // GATE-GUARD GUARDS (VG-*) — pipeline compliance
  // ══════════════════════════════════════════════════════════
  if (guardEnabled) {

    // ─── EXPLORE MODE (no active pipeline) ───
    if (!state) {
      if (WRITE_TOOLS.has(tool_name)) {
        if (targetFile.includes('.vela/')) {
          process.exit(0);
        }
        process.stderr.write(`[VG-EXPLORE] No active pipeline. Run: node .vela/cli/vela-engine.js init "<task>" --scale <small|medium|large>`);
        process.exit(2);
      }
      // Explore mode: reads, glob, grep all allowed
      process.exit(0);
    }

    // ─── DEVELOP MODE (pipeline active) ───

    // ─── VG-00: Block Claude's task system during pipeline ───
    const BLOCKED_TASK_TOOLS = new Set(['TaskCreate', 'TaskUpdate', 'TaskList', 'TaskGet']);
    if (BLOCKED_TASK_TOOLS.has(tool_name)) {
      process.stderr.write(`[VG-00] Claude task tools disabled during pipeline. Use Vela pipeline steps.`);
      process.exit(2);
    }

    // ─── Early-exit: non-write/non-Bash tools need no further guards ───
    // All VG-01 through VG-13 only check WRITE_TOOLS or Bash.
    // Read/Glob/Grep and other non-write tools can skip them entirely.
    if (!WRITE_TOOLS.has(tool_name) && tool_name !== 'Bash') {
      process.exit(0);
    }

    // ─── VG-01: Research before Plan ───
    if (WRITE_TOOLS.has(tool_name)) {
      const fileName = path.basename(targetFile);

      if (fileName === 'plan.md' && artifactDir) {
        const researchPath = path.join(artifactDir, 'research.md');
        if (!fs.existsSync(researchPath)) {
          process.stderr.write(`[VG-01] Cannot create plan without research. Complete research step first.`);
          process.exit(2);
        }
      }

      // ─── VG-04: Verification before Report ───
      if (fileName === 'report.md' && artifactDir) {
        const verificationPath = path.join(artifactDir, 'verification.md');
        if (!fs.existsSync(verificationPath)) {
          process.stderr.write(`[VG-04] Cannot create report without verification. Complete verification step first.`);
          process.exit(2);
        }
      }
    }

    // ─── VG-02: No source code edits before execute step ───
    if (WRITE_TOOLS.has(tool_name)) {
      // Skip .vela/ internal files — EXCEPT protected files
      if (targetFile.includes('.vela/')) {
        const protectedFile = path.basename(targetFile);
        // pipeline-state.json already handled by unified check above

        // ─── VG-11: approval/review files — team step only ───
        if (protectedFile.startsWith('approval-') || protectedFile.startsWith('review-')) {
          const hasTeam = stepDef && stepDef.team;
          if (!hasTeam) {
            process.stderr.write(`[VG-11] ${protectedFile} only allowed during team steps.`);
            process.exit(2);
          }
        }

        // Allow other .vela/ writes
        process.exit(0);
      }

      // Skip non-code files
      const ext = path.extname(targetFile).toLowerCase();
      if (!CODE_EXTENSIONS.has(ext)) {
        process.exit(0);
      }

      // Skip files in excluded paths
      const inSkipPath = SKIP_PATHS.some(sp => targetFile.includes(sp));
      if (inSkipPath) {
        process.exit(0);
      }

      // Check if we're at or past the execute step
      const executeReached = isStepReached(state, pipelineDef, 'execute');
      if (!executeReached) {
        process.stderr.write(`[VG-02] Source code edit before execute step. Complete: ${getStepsUntil(state, pipelineDef, 'execute').join(' → ')}`);
        process.exit(2);
      }

      // ─── VG-12: PM must delegate source edits in execute step ───
      if (currentStep === 'execute' && !isDelegated) {
        process.stderr.write(`[VG-12] PM cannot edit source directly in execute. Spawn a Subagent to implement.`);
        process.exit(2);
      }
    }

    // ─── VG-13: TDD sub-phase enforcement ───
    if (WRITE_TOOLS.has(tool_name) && currentStep === 'execute') {
      if (stepDef && Array.isArray(stepDef.sub_phases) && stepDef.sub_phases.length > 0) {
        const tddPhasePath = path.join(velaDir, 'state', 'tdd-phase.json');
        try {
          if (fs.existsSync(tddPhasePath)) {
            const tddState = JSON.parse(fs.readFileSync(tddPhasePath, 'utf-8'));
            const currentPhase = tddState.phase;

            if (currentPhase === 'test-write') {
              const basename = path.basename(targetFile);
              const isTestFile = /\.(test|spec)\./i.test(basename) ||
                targetFile.includes('__tests__/') ||
                targetFile.includes('/tests/') ||
                targetFile.startsWith('tests/');
              if (!isTestFile && !targetFile.includes('.vela/')) {
                const ext = path.extname(targetFile).toLowerCase();
                if (CODE_EXTENSIONS.has(ext)) {
                  process.stderr.write(`[VG-13] TDD phase "test-write" — only test files allowed. Write tests first, then transition to "implement".`);
                  process.exit(2);
                }
              }
            }
          }
        } catch (e) { /* corrupt JSON or read error → fail-open */ }
      }
    }

    // ─── VG-03: Build/test must pass before git commit ───
    if (tool_name === 'Bash') {
      const cmd = (tool_input.command || '').trim();
      if (/\bgit\s+commit\b/.test(cmd)) {
        const signalsPath = path.join(velaDir, 'tracker-signals.json');
        if (fs.existsSync(signalsPath)) {
          try {
            const signals = JSON.parse(fs.readFileSync(signalsPath, 'utf-8'));
            const recentFail = signals.some(s =>
              (s.type === 'build' || s.type === 'test') &&
              s.result === 'fail' &&
              Date.now() - s.timestamp < 5 * 60 * 1000
            );
            if (recentFail) {
              process.stderr.write(`[VG-03] Cannot commit with failed build/tests. Fix and re-run first.`);
              process.exit(2);
            }
          } catch (e) { /* signal file corrupt, allow commit */ }
        }
      }
    }

    // ─── VG-06: Revision limit enforcement ───
    if (WRITE_TOOLS.has(tool_name) && state.revisions) {
      if (stepDef && stepDef.max_revisions) {
        const currentRevisions = state.revisions[currentStep] || 0;
        if (currentRevisions >= stepDef.max_revisions) {
          process.stderr.write(`[VG-06] Revision limit reached for step "${currentStep}". Transition to next step or request approval.`);
          process.exit(2);
        }
      }
    }

    // ─── VG-07: Git commit only during execute/commit/finalize ───
    if (tool_name === 'Bash') {
      const cmd = (tool_input.command || '').trim();
      if (/\bgit\s+commit\b/.test(cmd) && state) {
        const allowedSteps = ['execute', 'commit', 'finalize'];
        if (!allowedSteps.includes(currentStep)) {
          process.stderr.write(`[VG-07] Git commit only allowed during execute/commit/finalize. Run: node .vela/cli/vela-engine.js commit`);
          process.exit(2);
        }
      }
    }

    // ─── VG-08: Git push only after verify ───
    if (tool_name === 'Bash') {
      const cmd = (tool_input.command || '').trim();
      if (/\bgit\s+push\b/.test(cmd) && !/\bgit\s+stash\s+push\b/.test(cmd) && state) {
        const verifyReached = isStepReached(state, pipelineDef, 'verify');
        if (!verifyReached) {
          process.stderr.write(`[VG-08] Git push only allowed after verification step. Complete verification first.`);
          process.exit(2);
        }
      }
    }

    // ─── VG-09: Protected branch commit warning ───
    if (tool_name === 'Bash') {
      const cmd = (tool_input.command || '').trim();
      if (/\bgit\s+commit\b/.test(cmd) && state && state.git) {
        const protectedBranches = ['main', 'master', 'develop'];
        const currentBranch = state.git.current_branch || state.git.base_branch;
        if (protectedBranches.includes(currentBranch)) {
          process.stdout.write(`[VG-09] WARNING: Committing to protected branch "${currentBranch}". Consider: git checkout -b vela/<feature> from main`);
        }
      }
    }
  }

  // All gates and guards passed
  process.exit(0);
}

/**
 * Check if a target step has been reached in the pipeline.
 */
function isStepReached(state, pipelineDef, targetStepId) {
  if (!pipelineDef || !state) return false;
  const pipelineType = state.pipeline_type || 'standard';
  const pipeline = pipelineDef.pipelines[pipelineType];
  if (!pipeline) return false;

  let steps = pipeline.steps;
  if (pipeline.inherits && pipeline.steps_only) {
    const parent = pipelineDef.pipelines[pipeline.inherits];
    if (parent) steps = parent.steps.filter(s => pipeline.steps_only.includes(s.id));
  }

  const currentIdx = steps.findIndex(s => s.id === state.current_step);
  const targetIdx = steps.findIndex(s => s.id === targetStepId);
  return currentIdx >= targetIdx;
}

/**
 * Get the list of steps from current to target.
 */
function getStepsUntil(state, pipelineDef, targetStepId) {
  if (!pipelineDef || !state) return [];
  const pipelineType = state.pipeline_type || 'standard';
  const pipeline = pipelineDef.pipelines[pipelineType];
  if (!pipeline) return [];

  let steps = pipeline.steps;
  if (pipeline.inherits && pipeline.steps_only) {
    const parent = pipelineDef.pipelines[pipeline.inherits];
    if (parent) steps = parent.steps.filter(s => pipeline.steps_only.includes(s.id));
  }

  const currentIdx = steps.findIndex(s => s.id === state.current_step);
  const targetIdx = steps.findIndex(s => s.id === targetStepId);
  if (currentIdx < 0 || targetIdx < 0) return [];

  return steps.slice(currentIdx, targetIdx + 1).map(s => s.name);
}

main().catch(() => process.exit(0));
