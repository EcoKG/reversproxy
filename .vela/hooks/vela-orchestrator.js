#!/usr/bin/env node
/**
 * Vela Orchestrator — UserPromptSubmit Hook
 *
 * Runs on every user message to inject Vela pipeline context into Claude's
 * awareness. This is how Claude knows what step it's on, what mode is active,
 * and what actions are permitted.
 *
 * Responsibilities:
 * 1. Inject current pipeline state and mode
 * 2. Show allowed next actions
 * 3. Detect crashed/stale pipelines and offer recovery
 * 4. Display session health on first prompt
 * 5. Inject team context during team steps (Teammate/Subagent roles)
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig, readPipelineDefinition, getCurrentStepDef, getSessionStatePath } = require('./shared/pipeline');

async function main() {
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0);
  }

  const { session_id, prompt, cwd } = input;
  if (!cwd) process.exit(0);

  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);

  // If Vela is not installed, stay silent
  if (!config || !config.sandbox || !config.sandbox.enabled) {
    process.exit(0);
  }

  const output = [];
  const sessionStatePath = getSessionStatePath(session_id, cwd);
  const isFirstPrompt = !fs.existsSync(sessionStatePath);

  // ─── Session Health Check (first prompt only) ───
  if (isFirstPrompt) {
    output.push('✦ Vela Engine v1.2 ✦');
    output.push(`⛵ Sandbox: ACTIVE | Mode: auto-detect`);

    const hooks = ['vela-gate-keeper', 'vela-gate-guard', 'vela-orchestrator', 'vela-tracker'];
    const hookStatus = hooks.map(h => {
      const hookPath = path.join(velaDir, 'hooks', `${h}.js`);
      return `${fs.existsSync(hookPath) ? '✓' : '✗'} ${h.replace('vela-', '')}`;
    });
    output.push(`🔭 Hooks: ${hookStatus.join(' | ')}`);
    output.push('✦─────────────────────✦');

    // Create session state + register PM session for GUARD 11
    try {
      const stateDir = path.dirname(sessionStatePath);
      if (!fs.existsSync(stateDir)) {
        fs.mkdirSync(stateDir, { recursive: true });
      }
      fs.writeFileSync(sessionStatePath, JSON.stringify({
        session_id,
        started: Date.now(),
        first_prompt: true,
        tool_count: 0
      }));

      // Register PM's session_id so Gate Guard can distinguish PM from subagents
      const pmSessionPath = path.join(velaDir, 'state', 'pm-session.json');
      fs.writeFileSync(pmSessionPath, JSON.stringify({
        session_id: session_id,
        registered_at: Date.now()
      }));
    } catch (e) {
      // Non-critical
    }
  }

  // ─── Persona Injection (always, regardless of pipeline) ───
  const personaPath = path.join(velaDir, 'persona.md');
  if (fs.existsSync(personaPath)) {
    const personaContent = fs.readFileSync(personaPath, 'utf-8').trim();
    if (personaContent) {
      output.push('');
      output.push('📝 Persona ──────────────────────────');
      output.push(personaContent);
      output.push('──────────────────────────────────────');
    }
  }

  // ─── Pipeline State Injection ───
  const state = findActivePipeline(velaDir);

  if (state) {
    const pipelineDef = readPipelineDefinition(cwd);
    const stepDef = getCurrentStepDef(pipelineDef, state);

    if (state._stale) {
      output.push('');
      output.push(`⚠ STALE PIPELINE DETECTED (idle >24h)`);
      output.push(`  Pipeline: ${state.pipeline_type || 'standard'}`);
      output.push(`  Last step: ${state.current_step}`);
      output.push(`  Resume or cancel the pipeline.`);
    }

    output.push('');
    output.push(`🧭 Pipeline ──────────────────────────`);
    output.push(`│ Type: ${state.pipeline_type || 'standard'}`);
    output.push(`│ Step: ${state.current_step} (${stepDef ? stepDef.name : 'unknown'})`);
    output.push(`│ Mode: ${stepDef ? stepDef.mode : 'read'}`);

    if (state.request) {
      output.push(`│ Task: ${state.request.substring(0, 80)}`);
    }

    // Show team step info (Agent Teams)
    if (stepDef && stepDef.team) {
      output.push(`│`);
      output.push(`│ 🌟 Team Step ────────────────────`);
      output.push(`│ │ Worker: ${stepDef.team.worker_role}`);
      output.push(`│ │ Reviewer: SDK (Haiku→Sonnet)`);
      output.push(`│ │   → node .vela/cli/vela-engine.js review`);
      if (stepDef.team.worker_role === 'executor') {
        output.push(`│ │ Executor: SDK (Sonnet)`);
        output.push(`│ │   → node .vela/cli/vela-engine.js execute`);
      }
      // Check for approval file (PM writes directly)
      if (state._artifactDir) {
        const approvalFile = `approval-${state.current_step}.json`;
        const approvalPath = path.join(state._artifactDir, approvalFile);
        const hasApproval = fs.existsSync(approvalPath);
        output.push(`│ │ Approval: ${hasApproval ? 'APPROVED' : 'pending'}`);
      }
      output.push(`│ └────────────────────────────────────`);
    }

    // Show next allowed actions
    if (stepDef) {
      const nextStep = getNextStep(state, pipelineDef);
      output.push(`│`);
      output.push(`│ Engine commands:`);

      if (!isFirstPrompt) {
        // Save tokens after first prompt
        output.push(`│   node .vela/cli/vela-engine.js state`);
        output.push(`│   node .vela/cli/vela-engine.js transition`);
      } else {
        output.push(`│   node .vela/cli/vela-engine.js state       — current status`);
        output.push(`│   node .vela/cli/vela-engine.js transition  — advance to next step`);
        output.push(`│   node .vela/cli/vela-engine.js dispatch    — get agent spec`);
        output.push(`│   node .vela/cli/vela-engine.js record      — save agent result`);
      }

      if (nextStep) {
        output.push(`│ Next: ${nextStep.name}`);
      }
    }

    // ─── Escalation Flag Injection (one-shot) ───
    try {
      const escalationPath = path.join(velaDir, 'state', 'escalation.json');
      if (fs.existsSync(escalationPath)) {
        const raw = fs.readFileSync(escalationPath, 'utf-8');
        const esc = JSON.parse(raw);
        output.push(`│`);
        output.push(`│ ⚠ ESCALATION: Reviewer 점수 ${esc.score}/${esc.threshold} (기준 미달). 다음 에이전트 소환 시 model: "opus" 사용. 기존 산출물을 컨텍스트로 전달.`);
        // One-shot: delete after injection to prevent duplicate prompts
        fs.unlinkSync(escalationPath);
      }
    } catch (e) {
      // Graceful skip on read/parse/delete failure
    }

    // ─── Auto Mode Directive Injection ───
    if (state.auto === true) {
      const isCheckpoint = state.current_step === 'checkpoint';
      output.push(`│`);
      if (isCheckpoint) {
        output.push(`│ ⚡ AUTO: plan-check 통과 확인 → record pass → transition 호출.`);
      } else {
        output.push(`│ ⚡ AUTO: 현재 단계를 완료한 뒤 즉시 transition을 호출하라.`);
      }
    } else if (state.auto === false && (state.auto_reject_count || 0) >= 2) {
      output.push(`│`);
      output.push(`│ ⚠ AUTO SUSPENDED: reject 반복으로 자동 진행이 중단되었다. 사용자에게 알려라.`);
    }

    output.push(`└──────────────────────────────────────`);
  } else {
    // No active pipeline — Explore mode
    output.push('');
    output.push(`⛵ Vela — Explore mode. Reads allowed, writes blocked.`);
    output.push(`  🧭 To modify code: node .vela/cli/vela-engine.js init "<task>" --scale <small|medium|large>`);
  }

  if (output.length > 0) {
    process.stdout.write(output.join('\n'));
  }

  process.exit(0);
}

function getNextStep(state, pipelineDef) {
  if (!pipelineDef || !state) return null;
  const pipelineType = state.pipeline_type || 'standard';
  const pipeline = pipelineDef.pipelines[pipelineType];
  if (!pipeline) return null;

  let steps = pipeline.steps;
  if (pipeline.inherits && pipeline.steps_only) {
    const parent = pipelineDef.pipelines[pipeline.inherits];
    if (parent) steps = parent.steps.filter(s => pipeline.steps_only.includes(s.id));
  }

  const currentIdx = steps.findIndex(s => s.id === state.current_step);
  if (currentIdx < 0 || currentIdx >= steps.length - 1) return null;
  return steps[currentIdx + 1];
}

main().catch(() => process.exit(0));
