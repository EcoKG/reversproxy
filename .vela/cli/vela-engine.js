#!/usr/bin/env node
/**
 * Vela Engine CLI — Pipeline State Management
 *
 * The engine is the single source of truth for pipeline state.
 * All state transitions happen through this CLI, never by direct file edits.
 *
 * Commands:
 *   init <request> [--type TYPE] [--scale SCALE]  — Start a new pipeline
 *   state                                          — Show current pipeline state
 *   transition                                     — Advance to the next step
 *   dispatch [--role ROLE]                         — Get agent specification
 *   record <verdict> [--summary TEXT]              — Record step result
 *   sub-transition                                 — Advance TDD sub-phase
 *   branch [--mode auto|prompt|none]               — Create feature branch
 *   commit [--message TEXT]                        — Commit changes
 *   cancel                                         — Cancel active pipeline
 *   review                                         — Run SDK 2-stage code review
 *
 * Team coordination uses Claude Code Agent Teams (SendMessage).
 * Approval tracked via file artifacts (approval-{step}.json).
 *
 * All commands output JSON to stdout.
 */

const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

const CWD = process.cwd();
const VELA_DIR = path.join(CWD, '.vela');
const ARTIFACTS_DIR = path.join(VELA_DIR, 'artifacts');
const TEMPLATES_DIR = path.join(VELA_DIR, 'templates');
const PROTECTED_BRANCHES = ['main', 'master', 'develop'];

// ─── Command Router ───
const args = process.argv.slice(2);
const command = args[0];

const commands = {
  init: cmdInit,
  state: cmdState,
  transition: cmdTransition,
  dispatch: cmdDispatch,
  record: cmdRecord,
  'sub-transition': cmdSubTransition,
  branch: cmdBranch,
  commit: cmdCommit,
  cancel: cmdCancel,
  history: cmdHistory,
  review: cmdReview,
  'plan-check': cmdPlanCheck,
  research: cmdResearch,
  execute: cmdExecute
};

if (!command || !commands[command]) {
  output({
    ok: false,
    error: `Unknown command: ${command || '(none)'}`,
    available: Object.keys(commands)
  });
  process.exit(1);
}

const result = commands[command]();
if (result && typeof result.then === 'function') {
  result.catch(err => {
    output({ ok: false, error: err.message });
    process.exit(1);
  });
}

// ─── Commands ───

function cmdInit() {
  const request = getArg(0) || getFlag('--request');
  if (!request) {
    return output({ ok: false, error: 'Request description required. Usage: vela-engine init "task description"' });
  }

  // Block if there's already an active pipeline
  const existing = findActiveState();
  if (existing && !hasFlag('--force')) {
    return output({
      ok: false,
      error: 'Active pipeline already exists.',
      current_step: existing.current_step,
      request: existing.request,
      hint: 'Complete or cancel the current pipeline first: vela-engine cancel'
    });
  }

  // Clean up cancelled artifacts older than 24 hours
  const cleaned = cleanupCancelledArtifacts(24);

  const type = getFlag('--type') || 'code';
  const scale = getFlag('--scale');

  // Scale selection is MANDATORY — user must choose
  if (!scale) {
    return output({
      ok: false,
      error: 'Pipeline scale selection required. Ask the user to choose.',
      options: {
        small: 'trivial pipeline (init → execute → commit → finalize) — 단일 파일, 10줄 이하',
        medium: 'quick pipeline (init → plan → execute → verify → commit → finalize) — 3파일 이하, 100줄 이하',
        large: 'standard pipeline (full 10-step with research, plan, team review) — 대규모 작업',
        ralph: 'ralph pipeline (테스트 통과까지 자동 반복) — 버그 수정, TDD',
        hotfix: 'hotfix pipeline (init → execute → commit) — 문서, 설정 등 비-소스 수정'
      },
      usage: 'vela-engine init "task" --scale small|medium|large|ralph|hotfix',
      message: 'User must select pipeline scale. Present the options and let them choose.'
    });
  }

  const pipelineType = scaleToPipeline(scale);

  // Load pipeline definition (before creating any directories)
  const pipelineDef = loadPipelineDefinition();
  if (!pipelineDef) {
    return output({ ok: false, error: 'Pipeline definition not found. Run vela-init first.' });
  }

  const steps = resolveSteps(pipelineDef, pipelineType);
  const firstStep = steps[0];

  // Git state snapshot
  const gitState = snapshotGitState();

  // Block if dirty tree (unless --force)
  if (gitState.is_repo && !gitState.is_clean && !hasFlag('--force')) {
    return output({
      ok: false,
      error: 'Working tree is dirty. Commit or stash changes before starting a pipeline.',
      git: gitState,
      hint: 'Use --force to skip this check, or run: git stash'
    });
  }

  // Ensure Vela files are hidden from git
  ensureGitignore();

  // Create artifact directory AFTER validation passes: {date}_{id}_{slug}
  const now = new Date();
  const dateStr = now.toISOString().split('T')[0];
  const uid = Math.random().toString(36).substring(2, 6);
  const slug = slugify(request);
  const artifactDir = path.join(ARTIFACTS_DIR, `${dateStr}_${uid}_${slug}`);

  fs.mkdirSync(artifactDir, { recursive: true });

  // Auto mode flag
  const autoMode = hasFlag('--auto');

  // Create pipeline state
  const state = {
    version: '1.1',
    status: 'active',
    pipeline_type: pipelineType,
    request: request,
    type: type,
    scale: scale,
    current_step: firstStep.id,
    current_step_index: 0,
    steps: steps.map(s => s.id),
    completed_steps: [],
    revisions: {},
    ...(autoMode ? { auto: true, auto_reject_count: 0 } : {}),
    git: gitState.is_repo ? {
      is_repo: true,
      base_branch: gitState.current_branch,
      current_branch: gitState.current_branch,
      pipeline_branch: null,
      checkpoint_hash: gitState.head_hash,
      commit_hash: null,
      stash_ref: gitState.stash_ref || null,
      remote: gitState.remote
    } : null,
    created_at: now.toISOString(),
    updated_at: now.toISOString()
  };

  // Create meta.json
  const meta = {
    request,
    type,
    scale,
    pipeline_type: pipelineType,
    created_at: now.toISOString()
  };

  writeJSON(path.join(artifactDir, 'pipeline-state.json'), state);
  writeJSON(path.join(artifactDir, 'meta.json'), meta);

  output({
    ok: true,
    command: 'init',
    pipeline_type: pipelineType,
    scale: scale,
    current_step: firstStep.id,
    current_mode: firstStep.mode,
    artifact_dir: artifactDir,
    steps: steps.map(s => ({ id: s.id, name: s.name, mode: s.mode })),
    cleaned_cancelled: cleaned,
    message: `Pipeline initialized. Current step: ${firstStep.name} (${firstStep.mode} mode)` +
      (cleaned > 0 ? ` (cleaned ${cleaned} cancelled artifact(s))` : '')
  });
}

function cmdState() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: true, command: 'state', active: false, message: 'No active pipeline.' });
  }

  const pipelineDef = loadPipelineDefinition();
  const steps = resolveSteps(pipelineDef, state.pipeline_type);
  const currentStepDef = steps.find(s => s.id === state.current_step);

  output({
    ok: true,
    command: 'state',
    active: true,
    pipeline_type: state.pipeline_type,
    request: state.request,
    current_step: state.current_step,
    current_step_name: currentStepDef ? currentStepDef.name : state.current_step,
    current_mode: currentStepDef ? currentStepDef.mode : 'read',
    completed_steps: state.completed_steps,
    remaining_steps: state.steps.filter(s => !state.completed_steps.includes(s)),
    revisions: state.revisions,
    sub_phase: state.sub_phases ? state.sub_phases[state.current_step] || null : null,
    git: state.git || null,
    artifact_dir: state._artifactDir
  });
}

function cmdTransition() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline to transition.' });
  }

  const pipelineDef = loadPipelineDefinition();
  const steps = resolveSteps(pipelineDef, state.pipeline_type);
  const currentIdx = steps.findIndex(s => s.id === state.current_step);

  if (currentIdx < 0) {
    return output({ ok: false, error: `Current step "${state.current_step}" not found in pipeline.` });
  }

  // Check exit gate for current step
  const currentStepDef = steps[currentIdx];
  const gateResult = checkExitGate(currentStepDef, state);
  if (!gateResult.passed) {
    return output({
      ok: false,
      error: `Exit gate not met for step "${state.current_step}"`,
      missing: gateResult.missing,
      message: `Complete these requirements before advancing: ${gateResult.missing.join(', ')}`
    });
  }

  // Mark current step as completed
  if (!state.completed_steps.includes(state.current_step)) {
    state.completed_steps.push(state.current_step);
  }

  // Check if this was the last step
  if (currentIdx >= steps.length - 1) {
    state.status = 'completed';
    state.current_step = 'done';
    state.updated_at = new Date().toISOString();
    writeJSON(state._path, cleanState(state));

    return output({
      ok: true,
      command: 'transition',
      completed: true,
      message: 'Pipeline completed successfully.'
    });
  }

  // Advance to next step
  const nextStep = steps[currentIdx + 1];
  state.current_step = nextStep.id;
  state.current_step_index = currentIdx + 1;
  state.updated_at = new Date().toISOString();

  // Agent Teams: no in-memory team state needed.
  // Team coordination is handled via Agent Teams (SendMessage)
  // and file-based artifacts (approval-{step}.json, review-{step}.md).

  // Initialize sub-phase tracking if step has sub_phases and tracking enabled
  if (nextStep.sub_phases && nextStep.sub_phase_tracking) {
    if (!state.sub_phases) state.sub_phases = {};
    state.sub_phases[nextStep.id] = {
      phases: nextStep.sub_phases,
      current_index: 0,
      current_phase: nextStep.sub_phases[0],
      completed_phases: []
    };
  }

  writeJSON(state._path, cleanState(state));

  output({
    ok: true,
    command: 'transition',
    previous_step: currentStepDef.id,
    current_step: nextStep.id,
    current_step_name: nextStep.name,
    current_mode: nextStep.mode,
    completed: false,
    message: `Advanced to: ${nextStep.name} (${nextStep.mode} mode)`
  });
}

function cmdDispatch() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  const role = getFlag('--role') || state.current_step;
  const pipelineDef = loadPipelineDefinition();
  const steps = resolveSteps(pipelineDef, state.pipeline_type);
  const stepDef = steps.find(s => s.id === state.current_step);

  output({
    ok: true,
    command: 'dispatch',
    step: state.current_step,
    role: role,
    mode: stepDef ? stepDef.mode : 'read',
    artifact_dir: state._artifactDir,
    context: {
      request: state.request,
      type: state.type,
      scale: state.scale,
      completed_steps: state.completed_steps,
      pipeline_type: state.pipeline_type
    }
  });
}

function cmdRecord() {
  const verdict = getArg(0);
  if (!verdict || !['pass', 'fail', 'reject'].includes(verdict.toLowerCase())) {
    return output({ ok: false, error: 'Verdict required: pass, fail, or reject' });
  }

  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  const summary = getFlag('--summary') || '';

  // Track revisions
  if (!state.revisions[state.current_step]) {
    state.revisions[state.current_step] = 0;
  }
  state.revisions[state.current_step]++;

  // Auto mode: reject counter and reset logic
  const verdictLower = verdict.toLowerCase();
  if (state.auto === true) {
    if (verdictLower === 'reject') {
      state.auto_reject_count = (state.auto_reject_count || 0) + 1;
      if (state.auto_reject_count >= 2) {
        state.auto = false;
      }
    } else if (verdictLower === 'pass' || verdictLower === 'approve') {
      state.auto_reject_count = 0;
    }
  }

  state.updated_at = new Date().toISOString();

  writeJSON(state._path, cleanState(state));

  const result = {
    ok: true,
    command: 'record',
    step: state.current_step,
    verdict: verdictLower,
    revision: state.revisions[state.current_step],
    summary: summary
  };

  // Warn when auto mode is disabled due to consecutive rejects
  if (state.auto === false && verdictLower === 'reject' && (state.auto_reject_count || 0) >= 2) {
    result.auto_disabled = true;
    result.auto_warning = '⚠️ Auto mode disabled: 2 consecutive rejects reached.';
  }

  output(result);
}

// cmdTeamDispatch and cmdTeamRecord REMOVED — replaced by Agent Teams.
// Team coordination now uses SendMessage between agents.
// Approval tracked via file-based artifacts (approval-{step}.json).

function cmdSubTransition() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  const currentStep = state.current_step;
  if (!state.sub_phases || !state.sub_phases[currentStep]) {
    return output({ ok: false, error: `Step "${currentStep}" does not have sub-phase tracking.` });
  }

  const sp = state.sub_phases[currentStep];
  const currentIdx = sp.current_index;

  if (currentIdx >= sp.phases.length - 1) {
    return output({
      ok: true,
      command: 'sub-transition',
      step: currentStep,
      completed: true,
      message: `All sub-phases completed for "${currentStep}".`
    });
  }

  // Mark current sub-phase as completed
  if (!sp.completed_phases.includes(sp.current_phase)) {
    sp.completed_phases.push(sp.current_phase);
  }

  // Advance
  const previousPhase = sp.current_phase;
  sp.current_index = currentIdx + 1;
  sp.current_phase = sp.phases[sp.current_index];

  state.updated_at = new Date().toISOString();
  writeJSON(state._path, cleanState(state));

  output({
    ok: true,
    command: 'sub-transition',
    step: currentStep,
    previous_phase: previousPhase,
    current_phase: sp.current_phase,
    remaining: sp.phases.slice(sp.current_index + 1),
    completed: false,
    message: `Sub-phase advanced: ${previousPhase} → ${sp.current_phase}`
  });
}

function cmdBranch() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  if (!state.git || !state.git.is_repo) {
    // Not a git repo — mark branch as skipped
    state.git = state.git || {};
    state.git.pipeline_branch = null;
    state.updated_at = new Date().toISOString();
    writeJSON(state._path, cleanState(state));
    return output({ ok: true, command: 'branch', skipped: true, message: 'Not a git repository. Branch step skipped.' });
  }

  const mode = getFlag('--mode') || 'auto';
  const currentBranch = gitExec('git rev-parse --abbrev-ref HEAD').trim();
  const isProtected = PROTECTED_BRANCHES.includes(currentBranch);

  // If already on a non-protected branch, use it
  if (!isProtected) {
    state.git.pipeline_branch = currentBranch;
    state.git.current_branch = currentBranch;
    state.updated_at = new Date().toISOString();
    writeJSON(state._path, cleanState(state));
    return output({
      ok: true,
      command: 'branch',
      action: 'existing',
      branch: currentBranch,
      message: `Already on non-protected branch "${currentBranch}". Using it as pipeline branch.`
    });
  }

  // Generate branch name
  const slug = slugify(state.request);
  const timeStr = new Date().toTimeString().substring(0, 5).replace(':', '');
  const branchName = `vela/${slug}-${timeStr}`;

  if (mode === 'none') {
    state.git.pipeline_branch = currentBranch;
    state.updated_at = new Date().toISOString();
    writeJSON(state._path, cleanState(state));
    return output({ ok: true, command: 'branch', action: 'none', branch: currentBranch, message: 'Branch creation skipped (mode: none).' });
  }

  if (mode === 'prompt') {
    return output({
      ok: true,
      command: 'branch',
      action: 'prompt',
      suggested_command: `git checkout -b ${branchName}`,
      message: `Run this command to create the pipeline branch: git checkout -b ${branchName}`
    });
  }

  // Auto mode: create branch
  try {
    gitExec(`git checkout -b ${branchName}`);
  } catch (e) {
    // Branch might exist, try checkout
    try {
      gitExec(`git checkout ${branchName}`);
    } catch (e2) {
      return output({ ok: false, error: `Failed to create branch: ${e2.message}` });
    }
  }

  state.git.pipeline_branch = branchName;
  state.git.current_branch = branchName;
  state.git.checkpoint_hash = gitExec('git rev-parse HEAD').trim();
  state.updated_at = new Date().toISOString();
  writeJSON(state._path, cleanState(state));

  output({
    ok: true,
    command: 'branch',
    action: 'created',
    branch: branchName,
    base_branch: state.git.base_branch,
    checkpoint_hash: state.git.checkpoint_hash,
    message: `Branch "${branchName}" created from "${state.git.base_branch}".`
  });
}

function cmdCommit() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  if (!state.git || !state.git.is_repo) {
    return output({ ok: true, command: 'commit', skipped: true, message: 'Not a git repository. Commit step skipped.' });
  }

  // Check for uncommitted changes
  const status = gitExec('git status --porcelain').trim();
  if (!status) {
    state.git.commit_hash = gitExec('git rev-parse HEAD').trim();
    state.updated_at = new Date().toISOString();
    writeJSON(state._path, cleanState(state));
    return output({ ok: true, command: 'commit', action: 'no_changes', message: 'No changes to commit.' });
  }

  // Generate conventional commit message
  const pipelineDef = loadPipelineDefinition();
  const typeMap = pipelineDef?.git?.commit?.type_map || {
    code: 'feat', 'code-bug': 'fix', 'code-refactor': 'refactor', docs: 'docs', infra: 'chore'
  };
  const commitType = typeMap[state.type] || 'feat';
  const slug = slugify(state.request);
  const shortDesc = state.request.substring(0, 70);

  const messageFlag = getFlag('--message');
  const commitMessage = messageFlag || `${commitType}(${slug}): ${shortDesc}`;
  const commitBody = `\nVela-Pipeline: ${path.basename(state._artifactDir)}\nCheckpoint: ${state.git.checkpoint_hash || 'unknown'}`;

  // Capture diff as artifact
  try {
    const diff = gitExec('git diff HEAD');
    if (diff && state._artifactDir) {
      fs.writeFileSync(path.join(state._artifactDir, 'diff.patch'), diff);
    }
  } catch (e) {}

  // Stage all changes (excluding .vela/ internals)
  try {
    gitExec('git add -A');
    // Unstage .vela/ internal files
    const velaFiles = [
      '.vela/cache/', '.vela/state/', '.vela/artifacts/',
      '.vela/tracker-signals.json', '.vela/write-log.jsonl'
    ];
    for (const vf of velaFiles) {
      try { gitExec(`git reset HEAD -- "${vf}"`); } catch (e) {}
    }
  } catch (e) {
    return output({ ok: false, error: `Failed to stage files: ${e.message}` });
  }

  // Commit
  const fullMessage = commitMessage + '\n' + commitBody;
  try {
    const tmpMsgFile = path.join(VELA_DIR, '_commit-msg.tmp');
    fs.writeFileSync(tmpMsgFile, fullMessage);
    gitExec(`git commit -F "${tmpMsgFile}"`);
    try { fs.unlinkSync(tmpMsgFile); } catch (e) {}
  } catch (e) {
    return output({ ok: false, error: `Commit failed: ${e.message}` });
  }

  const commitHash = gitExec('git rev-parse HEAD').trim();
  state.git.commit_hash = commitHash;
  state.updated_at = new Date().toISOString();
  writeJSON(state._path, cleanState(state));

  output({
    ok: true,
    command: 'commit',
    action: 'committed',
    hash: commitHash,
    message: commitMessage,
    branch: state.git.current_branch || state.git.pipeline_branch,
    files_in_diff: status.split('\n').length,
    message: `Committed: ${commitMessage} (${commitHash.substring(0, 7)})`
  });
}

function cmdCancel() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline to cancel.' });
  }

  state.status = 'cancelled';
  state.updated_at = new Date().toISOString();

  const recovery = {};
  if (state.git && state.git.is_repo) {
    recovery.checkpoint_hash = state.git.checkpoint_hash;
    recovery.pipeline_branch = state.git.pipeline_branch;
    recovery.base_branch = state.git.base_branch;
    recovery.hint = state.git.pipeline_branch
      ? `To discard pipeline branch: git checkout ${state.git.base_branch} && git branch -d ${state.git.pipeline_branch}`
      : `To see pipeline changes: git diff ${state.git.checkpoint_hash}..HEAD`;
  }

  writeJSON(state._path, cleanState(state));

  output({
    ok: true,
    command: 'cancel',
    recovery: recovery,
    message: 'Pipeline cancelled.'
  });
}

function cmdHistory() {
  if (!fs.existsSync(ARTIFACTS_DIR)) {
    return output({ ok: true, command: 'history', pipelines: [], message: 'No pipeline history.' });
  }

  const pipelines = [];
  try {
    const allDirs = fs.readdirSync(ARTIFACTS_DIR).sort().reverse();

    // Flat: {date}_{id}_{slug}/
    for (const dir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}_/.test(d))) {
      const dirPath = path.join(ARTIFACTS_DIR, dir);
      try { if (!fs.statSync(dirPath).isDirectory()) continue; } catch { continue; }
      const statePath = path.join(dirPath, 'pipeline-state.json');
      if (!fs.existsSync(statePath)) continue;
      try {
        const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
        pipelines.push({
          date: dir.split('_')[0], slug: dir,
          status: state.status, type: state.pipeline_type,
          request: (state.request || '').substring(0, 60),
          step: state.current_step,
          steps_completed: (state.completed_steps || []).length,
          steps_total: (state.steps || []).length,
          created: state.created_at, updated: state.updated_at
        });
      } catch (e) {}
    }

    // Backward compat: {date}/{slug}/
    for (const dateDir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d))) {
      const datePath = path.join(ARTIFACTS_DIR, dateDir);
      let slugDirs;
      try { slugDirs = fs.readdirSync(datePath).filter(d => fs.statSync(path.join(datePath, d)).isDirectory()); } catch { continue; }
      for (const slugDir of slugDirs) {
        const statePath = path.join(datePath, slugDir, 'pipeline-state.json');
        if (!fs.existsSync(statePath)) continue;
        try {
          const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
          pipelines.push({
            date: dateDir, slug: slugDir,
            status: state.status, type: state.pipeline_type,
            request: (state.request || '').substring(0, 60),
            step: state.current_step,
            steps_completed: (state.completed_steps || []).length,
            steps_total: (state.steps || []).length,
            created: state.created_at, updated: state.updated_at
          });
        } catch (e) {}
      }
    }
  } catch (e) {}

  output({
    ok: true,
    command: 'history',
    count: pipelines.length,
    pipelines: pipelines
  });
}

async function cmdReview() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  // Validate current step has a reviewer_role
  const pipelineDef = loadPipelineDefinition();
  const steps = resolveSteps(pipelineDef, state.pipeline_type);
  const currentStepDef = steps.find(s => s.id === state.current_step);

  if (!currentStepDef || !currentStepDef.team || !currentStepDef.team.reviewer_role) {
    return output({
      ok: false,
      error: 'Current step does not have a reviewer role.',
      current_step: state.current_step
    });
  }

  const artifactDir = state._artifactDir;
  if (!artifactDir) {
    return output({ ok: false, error: 'No artifact directory found for active pipeline.' });
  }

  const { sdkReview } = require('../hooks/shared/sdk-reviewer.js');
  const result = await sdkReview({
    step: state.current_step,
    artifactDir,
    cwd: CWD
  });

  output({
    ok: result.ok,
    command: 'review',
    ...result
  });
}

async function cmdPlanCheck() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  const artifactDir = state._artifactDir;
  if (!artifactDir) {
    return output({ ok: false, error: 'No artifact directory found for active pipeline.' });
  }

  // Check plan.md exists (entry gate equivalent)
  const planPath = path.join(artifactDir, 'plan.md');
  if (!fs.existsSync(planPath)) {
    return output({
      ok: false,
      error: 'plan.md not found in artifact directory.',
      artifactDir
    });
  }

  const { sdkPlanCheck } = require('../hooks/shared/sdk-plan-checker.js');
  const result = await sdkPlanCheck({ artifactDir, cwd: CWD });

  output({
    ok: result.ok,
    command: 'plan-check',
    ...result
  });
}

async function cmdExecute() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  // Validate current step has an executor worker_role
  const pipelineDef = loadPipelineDefinition();
  const steps = resolveSteps(pipelineDef, state.pipeline_type);
  const currentStepDef = steps.find(s => s.id === state.current_step);

  if (!currentStepDef || !currentStepDef.team || currentStepDef.team.worker_role !== 'executor') {
    return output({
      ok: false,
      error: 'Current step does not have an executor worker role.',
      current_step: state.current_step
    });
  }

  const artifactDir = state._artifactDir;
  if (!artifactDir) {
    return output({ ok: false, error: 'No artifact directory found for active pipeline.' });
  }

  const { sdkExecute } = require('../hooks/shared/sdk-executor.js');
  const result = await sdkExecute({
    step: state.current_step,
    artifactDir,
    cwd: CWD
  });

  output({
    ok: result.ok,
    command: 'execute',
    ...result
  });
}

async function cmdResearch() {
  const state = findActiveState();
  if (!state) {
    return output({ ok: false, error: 'No active pipeline.' });
  }

  const artifactDir = state._artifactDir;
  if (!artifactDir) {
    return output({ ok: false, error: 'No artifact directory found for active pipeline.' });
  }

  const step = state.current_step || 'unknown';

  const { sdkResearch } = require('../hooks/shared/sdk-researcher.js');
  const result = await sdkResearch({ step, artifactDir, cwd: CWD });

  output({
    ok: result.ok,
    command: 'research',
    ...result
  });
}

// ─── Helpers ───

// getOrCreateTeam REMOVED — Agent Teams handles team state via SendMessage.

function findActiveState() {
  if (!fs.existsSync(ARTIFACTS_DIR)) return null;

  try {
    const allDirs = fs.readdirSync(ARTIFACTS_DIR).sort().reverse();

    // Flat: {date}_{id}_{slug}/
    for (const dir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}_/.test(d))) {
      const dirPath = path.join(ARTIFACTS_DIR, dir);
      try { if (!fs.statSync(dirPath).isDirectory()) continue; } catch { continue; }
      const statePath = path.join(dirPath, 'pipeline-state.json');
      if (!fs.existsSync(statePath)) continue;
      try {
        const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
        if (state.status === 'completed' || state.status === 'cancelled') continue;
        state._path = statePath;
        state._artifactDir = dirPath;
        return state;
      } catch (e) { continue; }
    }

    // Backward compat: {date}/{slug}/
    for (const dateDir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d))) {
      const datePath = path.join(ARTIFACTS_DIR, dateDir);
      let slugDirs;
      try { slugDirs = fs.readdirSync(datePath).filter(d => fs.statSync(path.join(datePath, d)).isDirectory()); } catch { continue; }
      for (const slugDir of slugDirs) {
        const statePath = path.join(datePath, slugDir, 'pipeline-state.json');
        if (!fs.existsSync(statePath)) continue;
        try {
          const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
          if (state.status === 'completed' || state.status === 'cancelled') continue;
          state._path = statePath;
          state._artifactDir = path.join(datePath, slugDir);
          return state;
        } catch (e) { continue; }
      }
    }
  } catch (e) {}

  return null;
}

function loadPipelineDefinition() {
  const pipelinePath = path.join(TEMPLATES_DIR, 'pipeline.json');
  if (!fs.existsSync(pipelinePath)) return null;
  try {
    return JSON.parse(fs.readFileSync(pipelinePath, 'utf-8'));
  } catch (e) {
    return null;
  }
}

function resolveSteps(pipelineDef, pipelineType) {
  if (!pipelineDef) return [];
  const pipeline = pipelineDef.pipelines[pipelineType || 'standard'];
  if (!pipeline) return [];

  let steps = pipeline.steps;
  if (pipeline.inherits && pipeline.steps_only) {
    const parent = pipelineDef.pipelines[pipeline.inherits];
    if (parent) {
      steps = parent.steps.filter(s => pipeline.steps_only.includes(s.id));
      if (pipeline.overrides) {
        steps = steps.map(s => pipeline.overrides[s.id] ? { ...s, ...pipeline.overrides[s.id] } : s);
      }
    }
  }

  return steps;
}

function checkExitGate(stepDef, state) {
  if (!stepDef || !stepDef.exit_gate || stepDef.exit_gate.length === 0) {
    return { passed: true, missing: [] };
  }

  const artifactDir = state._artifactDir;
  const missing = [];

  for (const gate of stepDef.exit_gate) {
    switch (gate) {
      case 'artifact_dir_created':
        if (!artifactDir || !fs.existsSync(artifactDir)) missing.push(gate);
        break;
      case 'mode_detected':
        // Always passes after init
        break;
      case 'init_complete':
        if (!state.completed_steps.includes('init')) missing.push(gate);
        break;
      case 'research_md_exists':
        if (!artifactDir || !fs.existsSync(path.join(artifactDir, 'research.md'))) missing.push(gate);
        break;
      case 'plan_md_exists':
        if (!artifactDir || !fs.existsSync(path.join(artifactDir, 'plan.md'))) missing.push(gate);
        break;
      case 'plan_check_pass':
        if (!artifactDir || !fs.existsSync(path.join(artifactDir, 'plan-check.md'))) missing.push(gate);
        break;
      case 'user_approved':
        // Checkpoint is acknowledged when a record has been made for this step.
        // We check revisions instead of completed_steps because transition()
        // adds to completed_steps AFTER the exit gate check.
        if (state.current_step === 'checkpoint' && (!state.revisions.checkpoint || state.revisions.checkpoint < 1)) {
          // Auto mode: if plan-check.md exists, auto-pass the checkpoint
          if (state.auto === true && state.current_step === 'checkpoint' &&
              artifactDir && fs.existsSync(path.join(artifactDir, 'plan-check.md'))) {
            // plan-check gate passed — auto-approve checkpoint
            break;
          }
          missing.push(gate);
        }
        break;
      case 'plan_architecture_complete':
        // Standard pipeline: plan.md must contain architecture sections with substance
        if (artifactDir && fs.existsSync(path.join(artifactDir, 'plan.md'))) {
          const planContent = fs.readFileSync(path.join(artifactDir, 'plan.md'), 'utf-8');
          const requiredSections = ['## Architecture', '## Class Specification', '## Test Strategy'];
          for (const section of requiredSections) {
            if (!planContent.includes(section)) {
              missing.push(`plan_missing_section:${section}`);
            } else {
              // Check section has substance (not just a header)
              const sectionIdx = planContent.indexOf(section);
              const nextSectionIdx = planContent.indexOf('\n## ', sectionIdx + section.length);
              const sectionContent = nextSectionIdx > 0
                ? planContent.substring(sectionIdx + section.length, nextSectionIdx)
                : planContent.substring(sectionIdx + section.length);
              if (sectionContent.trim().length < 200) {
                missing.push(`plan_section_too_short:${section}`);
              }
            }
          }
        }
        break;
      case 'approval_exists':
      case 'leader_approved': // backward compatibility
        // File-based: PM writes approval-{step}.json with decision: "approve"
        if (artifactDir) {
          const approvalPath = path.join(artifactDir, `approval-${state.current_step}.json`);
          if (!fs.existsSync(approvalPath)) {
            missing.push(`approval_missing:approval-${state.current_step}.json`);
          } else {
            try {
              const approval = JSON.parse(fs.readFileSync(approvalPath, 'utf-8'));
              if (approval.decision !== 'approve') {
                missing.push(`rejected:${state.current_step}`);
              }
            } catch (e) {
              missing.push(`approval_invalid:${state.current_step}`);
            }
          }
        }
        break;
      case 'review_exists':
      case 'leader_review_exists': // backward compatibility
        // Reviewer subagent writes review-{step}.md
        if (artifactDir) {
          const reviewPath = path.join(artifactDir, `review-${state.current_step}.md`);
          if (!fs.existsSync(reviewPath)) {
            missing.push(`review_missing:review-${state.current_step}.md`);
          }
        }
        break;
      case 'implementation_complete':
        // File-based: approval-execute.json must exist with decision: "approve"
        if (artifactDir) {
          const execApprovalPath = path.join(artifactDir, 'approval-execute.json');
          if (!fs.existsSync(execApprovalPath)) {
            missing.push('approval_missing:approval-execute.json');
          } else {
            try {
              const approval = JSON.parse(fs.readFileSync(execApprovalPath, 'utf-8'));
              if (approval.decision !== 'approve') {
                missing.push('rejected:execute');
              }
            } catch (e) {
              missing.push('approval_invalid:execute');
            }
          }
        }
        break;
      case 'git_clean':
        // Init gate: working tree must be clean (checked during init, always passes after)
        break;
      case 'branch_created':
        // Branch gate: pipeline branch recorded in state
        if (state.git && state.git.is_repo) {
          if (!state.git.pipeline_branch && state.current_step === 'branch') {
            // Check if branch step was recorded (revisions > 0)
            if (!state.revisions.branch || state.revisions.branch < 1) {
              missing.push(gate);
            }
          }
        }
        break;
      case 'changes_committed':
        // Commit gate: commit hash recorded in state
        if (state.git && state.git.is_repo) {
          if (!state.git.commit_hash && state.current_step === 'commit') {
            if (!state.revisions.commit || state.revisions.commit < 1) {
              missing.push(gate);
            }
          }
        }
        break;
      case 'verification_md_exists':
        if (!artifactDir || !fs.existsSync(path.join(artifactDir, 'verification.md'))) missing.push(gate);
        break;
      case 'report_md_exists':
        // Finalize gate - report is the output of this step
        break;
      default:
        // Unknown gate, skip
        break;
    }
  }

  return { passed: missing.length === 0, missing };
}

function autoDetectScale(request) {
  const words = request.split(/\s+/).length;
  if (words <= 10) return 'small';
  if (words <= 30) return 'medium';
  return 'large';
}

function scaleToPipeline(scale) {
  switch (scale) {
    case 'small': return 'trivial';
    case 'medium': return 'quick';
    case 'large': return 'standard';
    case 'ralph': return 'ralph';
    case 'hotfix': return 'hotfix';
    default: return 'standard';
  }
}

function slugify(text) {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9가-힣\s-]/g, '')
    .replace(/\s+/g, '-')
    .substring(0, 30)
    .replace(/-+$/, '');
}

function cleanState(state) {
  const clean = { ...state };
  delete clean._path;
  delete clean._artifactDir;
  delete clean._stale;
  return clean;
}

function writeJSON(filePath, data) {
  const dir = path.dirname(filePath);
  if (!fs.existsSync(dir)) fs.mkdirSync(dir, { recursive: true });
  const tmpPath = filePath + '.tmp';
  fs.writeFileSync(tmpPath, JSON.stringify(data, null, 2));
  fs.renameSync(tmpPath, filePath);
}

function output(data) {
  process.stdout.write(JSON.stringify(data, null, 2));
}

function getArg(index) {
  return args[index + 1] || null;  // +1 because args[0] is the command
}

function getFlag(flag) {
  const idx = args.indexOf(flag);
  return idx >= 0 && idx < args.length - 1 ? args[idx + 1] : null;
}

function hasFlag(flag) {
  return args.includes(flag);
}

// ─── Cleanup ───

/**
 * Remove cancelled pipeline artifact directories older than `hoursOld` hours.
 * Only deletes directories where pipeline-state.json has status: "cancelled".
 * Completed pipelines are preserved (they contain reports and history).
 * Returns count of cleaned directories.
 */
function cleanupCancelledArtifacts(hoursOld) {
  if (!fs.existsSync(ARTIFACTS_DIR)) return 0;

  const cutoff = Date.now() - (hoursOld * 60 * 60 * 1000);
  let cleaned = 0;

  function tryCleanDir(dirPath) {
    const statePath = path.join(dirPath, 'pipeline-state.json');

    // Remove empty artifact directories (no pipeline-state.json = never used)
    if (!fs.existsSync(statePath)) {
      try {
        const files = fs.readdirSync(dirPath);
        if (files.length === 0) {
          fs.rmdirSync(dirPath);
          cleaned++;
        }
      } catch (e) {}
      return;
    }

    try {
      const state = JSON.parse(fs.readFileSync(statePath, 'utf-8'));
      // Clean cancelled and completed pipelines older than cutoff
      if (state.status !== 'cancelled' && state.status !== 'completed') return;
      const mtime = fs.statSync(statePath).mtimeMs;
      if (mtime > cutoff) return;
      fs.rmSync(dirPath, { recursive: true, force: true });
      cleaned++;
    } catch (e) {}
  }

  try {
    const allDirs = fs.readdirSync(ARTIFACTS_DIR);

    // Flat: {date}_{id}_{slug}/
    for (const dir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}_/.test(d))) {
      const dirPath = path.join(ARTIFACTS_DIR, dir);
      try { if (!fs.statSync(dirPath).isDirectory()) continue; } catch { continue; }
      tryCleanDir(dirPath);
    }

    // Backward compat: {date}/{slug}/
    for (const dateDir of allDirs.filter(d => /^\d{4}-\d{2}-\d{2}$/.test(d))) {
      const datePath = path.join(ARTIFACTS_DIR, dateDir);
      let slugDirs;
      try { slugDirs = fs.readdirSync(datePath).filter(d => fs.statSync(path.join(datePath, d)).isDirectory()); } catch { continue; }
      for (const slugDir of slugDirs) {
        tryCleanDir(path.join(datePath, slugDir));
      }
      // Clean empty date dirs
      try { if (fs.readdirSync(datePath).length === 0) fs.rmdirSync(datePath); } catch (e) {}
    }
  } catch (e) {}

  return cleaned;
}

// ─── Git Helpers ───

function gitExec(cmd) {
  return execSync(cmd, { cwd: CWD, stdio: ['pipe', 'pipe', 'pipe'], timeout: 15000 }).toString();
}

function snapshotGitState() {
  try {
    gitExec('git rev-parse --git-dir');
  } catch (e) {
    return { is_repo: false };
  }

  try {
    const currentBranch = gitExec('git rev-parse --abbrev-ref HEAD').trim();
    const status = gitExec('git status --porcelain').trim();
    const headHash = gitExec('git rev-parse HEAD').trim();

    let remote = null;
    try {
      remote = gitExec('git remote').trim().split('\n')[0] || null;
    } catch (e) {}

    return {
      is_repo: true,
      current_branch: currentBranch,
      is_clean: status === '',
      dirty_files: status ? status.split('\n').length : 0,
      head_hash: headHash,
      remote: remote,
      is_protected: PROTECTED_BRANCHES.includes(currentBranch)
    };
  } catch (e) {
    return { is_repo: true, error: e.message };
  }
}

function ensureGitignore() {
  const gitignorePath = path.join(CWD, '.gitignore');
  const velaEntries = [
    '# Vela Engine (auto-managed)',
    '.vela/',
    '.claude/',
    'CLAUDE.md'
  ];

  // Step 1: Remove already-tracked Vela files BEFORE updating .gitignore
  // (if .gitignore lists them first, git silently drops the staged deletions)
  try {
    const tracked = execSync('git ls-files .vela/ .claude/ CLAUDE.md', {
      cwd: CWD, stdio: ['pipe', 'pipe', 'pipe'], timeout: 5000
    }).toString().trim();
    if (tracked) {
      execSync('git rm -r --cached --ignore-unmatch .vela/ .claude/ CLAUDE.md', {
        cwd: CWD, stdio: 'pipe', timeout: 10000
      });
      execSync('git commit -m "chore: untrack Vela files from git" --no-verify', {
        cwd: CWD, stdio: 'pipe', timeout: 10000
      });
    }
  } catch (e) {
    // Not a git repo, git not available, or nothing to commit — skip
  }

  // Step 2: Update .gitignore (after deletions are committed)
  let content = '';
  if (fs.existsSync(gitignorePath)) {
    content = fs.readFileSync(gitignorePath, 'utf-8');
  }

  const missingEntries = velaEntries.filter(entry =>
    !entry.startsWith('#') && !content.includes(entry)
  );

  if (missingEntries.length > 0) {
    if (!content.includes('# Vela Engine')) {
      fs.appendFileSync(gitignorePath, '\n' + velaEntries.join('\n') + '\n');
    } else {
      fs.appendFileSync(gitignorePath, missingEntries.join('\n') + '\n');
    }
  }
}
