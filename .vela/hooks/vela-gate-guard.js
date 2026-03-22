#!/usr/bin/env node
/**
 * Vela Gate Guard (가이드라인) — PreToolUse Hook
 *
 * Enforces pipeline compliance. When Claude's actions deviate from
 * the pipeline, Gate Guard blocks and redirects back to the correct path.
 *
 * This guard CANNOT be ignored, bypassed, or circumvented.
 *
 * Responsibilities:
 * 1. Enforce artifact ordering (no plan without research, etc.)
 * 2. Block source code edits before execute step
 * 3. Block commits with failed builds/tests
 * 4. Enforce verification before report
 * 5. Track revision limits per step
 *
 * Exit codes:
 *   0 — Action permitted
 *   2 — Action blocked (hard block, pipeline violation)
 *
 * stdout — Non-blocking guidance messages
 * stderr — Block reason (when exit 2)
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readPipelineDefinition, getCurrentStepDef, readConfig } = require('./shared/pipeline');
const { CODE_EXTENSIONS, SKIP_PATHS, WRITE_TOOLS, READ_TOOLS } = require('./shared/constants');

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0);
  }

  const { tool_name, tool_input, session_id, cwd } = input;
  if (!tool_name || !cwd) process.exit(0);

  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);
  if (!config || !config.gate_guard || !config.gate_guard.enabled) {
    process.exit(0);
  }

  const state = findActivePipeline(velaDir);

  // ─── EXPLORE MODE (no active pipeline) ───
  if (!state) {
    // Block writes (existing behavior)
    if (WRITE_TOOLS.has(tool_name)) {
      const targetFile = tool_input.file_path || tool_input.path || '';
      if (targetFile.includes('.vela/')) {
        process.exit(0);
      }
      process.stderr.write(
        `🌟 [Vela] ✦ BLOCKED: No active pipeline (Explore mode).\n` +
        `  Tool: ${tool_name}\n` +
        `  To modify code: node .vela/cli/vela-engine.js init "<task>" --scale <small|medium|large>`
      );
      process.exit(2);
    }
    // Explore mode: reads, glob, grep all allowed
    process.exit(0);
  }

  // ─── DEVELOP MODE (pipeline active) ───

  // ─── GUARD 0: Block Claude's task system during pipeline ───
  // Claude uses TaskCreate/TaskUpdate to build competing plans that bypass the pipeline.
  const BLOCKED_TASK_TOOLS = new Set(['TaskCreate', 'TaskUpdate', 'TaskList', 'TaskGet']);
  if (BLOCKED_TASK_TOOLS.has(tool_name)) {
    process.stderr.write(
      `🌟 [Vela] ✦ BLOCKED: Claude task tools disabled during Vela pipeline.\n` +
      `  Use Vela pipeline steps instead.\n` +
      `  Current step: ${state.current_step}\n` +
      `  Advance with: node .vela/cli/vela-engine.js transition`
    );
    process.exit(2);
  }

  // ─── GUARD 0.5: Read-throttle warning in non-research steps ───
  if (READ_TOOLS.has(tool_name) && state.current_step !== 'research') {
    const counterPath = path.join(velaDir, 'state', 'reads-since-transition.json');
    try {
      if (fs.existsSync(counterPath)) {
        const counter = JSON.parse(fs.readFileSync(counterPath, 'utf-8'));
        if (counter.step === state.current_step && counter.count > 5) {
          process.stdout.write(
            `🌟 [Vela] ⚠ WARNING: ${counter.count} reads in "${state.current_step}" step.\n` +
            `  Extensive reading belongs in the research step.\n` +
            `  Current step: ${state.current_step}`
          );
        }
      }
    } catch (e) {}
  }

  const pipelineDef = readPipelineDefinition(cwd);
  const currentStep = state.current_step;
  const artifactDir = state._artifactDir;

  // ─── GUARD 1: Research before Plan ───
  // Cannot create plan.md without research.md existing first
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';
    const fileName = path.basename(targetFile);

    if (fileName === 'plan.md' && artifactDir) {
      const researchPath = path.join(artifactDir, 'research.md');
      if (!fs.existsSync(researchPath)) {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Cannot create plan without research.\n` +
          `  research.md must exist before plan.md.\n` +
          `  Current step: ${currentStep}\n` +
          `  Complete the research step first.`
        );
        process.exit(2);
      }
    }
  }

  // ─── GUARD 2: No source code edits before execute step ───
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';

    // Skip .vela/ internal files — EXCEPT protected files
    if (targetFile.includes('.vela/')) {
      const protectedFile = path.basename(targetFile);
      // pipeline-state.json MUST go through GUARD 5 (engine-managed only)
      // approval-*.json MUST be written by Leader agent, not PM
      if (protectedFile === 'pipeline-state.json') {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Cannot directly modify pipeline-state.json.\n` +
          `  Pipeline state is managed exclusively by the Vela engine.\n` +
          `  Use: node .vela/cli/vela-engine.js transition`
        );
        process.exit(2);
      }
      // Allow other .vela/ writes (artifacts, cache, etc.)
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
      process.stderr.write(
        `🌟 [Vela] ✦ BLOCKED: Source code modification before execute step.\n` +
        `  File: ${targetFile}\n` +
        `  Current step: ${currentStep}\n` +
        `  Source code can only be modified during the execute step.\n` +
        `  Complete the pipeline steps: ${getStepsUntil(state, pipelineDef, 'execute').join(' → ')}`
      );
      process.exit(2);
    }

    // GUARD 10 REMOVED — Agent Teams provides agent isolation.
    // Each agent (Executor, Researcher, etc.) runs as a separate Claude instance
    // with its own CLAUDE.md defining what it can/cannot do.
  }

  // ─── GUARD 3: Build/test must pass before git commit ───
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
            Date.now() - s.timestamp < 5 * 60 * 1000 // Within 5 minutes
          );
          if (recentFail) {
            process.stderr.write(
              `🌟 [Vela] ✦ BLOCKED: Cannot commit with failed build/tests.\n` +
              `  Recent build or test failure detected.\n` +
              `  Fix the issues and re-run tests before committing.`
            );
            process.exit(2);
          }
        } catch (e) {
          // Signal file corrupt, allow commit
        }
      }
    }
  }

  // ─── GUARD 4: Verification before Report ───
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';
    const fileName = path.basename(targetFile);

    if (fileName === 'report.md' && artifactDir) {
      const verificationPath = path.join(artifactDir, 'verification.md');
      if (!fs.existsSync(verificationPath)) {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Cannot create report without verification.\n` +
          `  verification.md must exist before report.md.\n` +
          `  Complete the verification step first.`
        );
        process.exit(2);
      }
    }
  }

  // ─── GUARD 5: Pipeline state is engine-managed only ───
  if (WRITE_TOOLS.has(tool_name)) {
    const targetFile = tool_input.file_path || tool_input.path || '';
    const fileName = path.basename(targetFile);

    if (fileName === 'pipeline-state.json') {
      process.stderr.write(
        `🌟 [Vela] ✦ BLOCKED: Cannot directly modify pipeline-state.json.\n` +
        `  Pipeline state is managed exclusively by the Vela engine.\n` +
        `  Use the engine CLI: node .vela/cli/vela-engine.js transition`
      );
      process.exit(2);
    }
  }

  // ─── GUARD 6: Revision limit enforcement ───
  if (WRITE_TOOLS.has(tool_name) && state.revisions) {
    const stepDef = getCurrentStepDef(pipelineDef, state);
    if (stepDef && stepDef.max_revisions) {
      const currentRevisions = state.revisions[currentStep] || 0;
      if (currentRevisions >= stepDef.max_revisions) {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Revision limit reached for step "${currentStep}".\n` +
          `  Max revisions: ${stepDef.max_revisions}\n` +
          `  Current revisions: ${currentRevisions}\n` +
          `  Transition to the next step or request user approval to continue.`
        );
        process.exit(2);
      }
    }
  }

  // ─── GUARD 7: Git commit only during execute/commit/finalize ───
  if (tool_name === 'Bash') {
    const cmd = (tool_input.command || '').trim();
    if (/\bgit\s+commit\b/.test(cmd) && state) {
      const allowedSteps = ['execute', 'commit', 'finalize'];
      if (!allowedSteps.includes(currentStep)) {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Git commit only allowed during execute/commit/finalize steps.\n` +
          `  Current step: ${currentStep}\n` +
          `  Use the Vela engine: node .vela/cli/vela-engine.js commit`
        );
        process.exit(2);
      }
    }
  }

  // ─── GUARD 8: Git push only after verify ───
  if (tool_name === 'Bash') {
    const cmd = (tool_input.command || '').trim();
    if (/\bgit\s+push\b/.test(cmd) && !/\bgit\s+stash\s+push\b/.test(cmd) && state) {
      const verifyReached = isStepReached(state, pipelineDef, 'verify');
      if (!verifyReached) {
        process.stderr.write(
          `🌟 [Vela] ✦ BLOCKED: Git push only allowed after verification step.\n` +
          `  Current step: ${currentStep}\n` +
          `  Complete verification before pushing.`
        );
        process.exit(2);
      }
    }
  }

  // ─── GUARD 9: Protected branch commit warning ───
  if (tool_name === 'Bash') {
    const cmd = (tool_input.command || '').trim();
    if (/\bgit\s+commit\b/.test(cmd) && state && state.git) {
      const protectedBranches = ['main', 'master', 'develop'];
      const currentBranch = state.git.current_branch || state.git.base_branch;
      if (protectedBranches.includes(currentBranch)) {
        process.stdout.write(
          `🌟 [Vela] ⚠ WARNING: Committing to protected branch "${currentBranch}".\n` +
          `  Consider creating a feature branch: git checkout -b vela/<feature-name>`
        );
      }
    }
  }

  // All guards passed
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
