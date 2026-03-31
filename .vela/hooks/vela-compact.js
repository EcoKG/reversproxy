#!/usr/bin/env node
/**
 * ⛵ Vela Compact Hook — Preserves pipeline state through context compression
 *
 * PreCompact: Saves current pipeline state summary to file
 * PostCompact: Re-injects pipeline state into context
 *
 * Used for BOTH PreCompact and PostCompact events.
 * Detects which event via the hook input.
 */

const fs = require('fs');
const path = require('path');
const { findActivePipeline, readConfig } = require('./shared/pipeline');

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

  const state = findActivePipeline(velaDir);
  if (!state) process.exit(0);

  const compactFile = path.join(velaDir, 'state', 'compact-context.json');
  const stateDir = path.join(velaDir, 'state');

  // Determine if PreCompact or PostCompact based on whether compact-context exists
  // PreCompact: save state
  // PostCompact: inject state (file exists from PreCompact)

  const step = state.current_step;
  const ptype = state.pipeline_type;
  const request = state.request;
  const artifactDir = state._artifactDir;

  // Always save/update the compact context
  if (!fs.existsSync(stateDir)) fs.mkdirSync(stateDir, { recursive: true });

  const compactContext = {
    pipeline_type: ptype,
    current_step: step,
    request: request,
    completed_steps: state.completed_steps || [],
    artifact_dir: artifactDir,
    git: state.git || null,
    saved_at: new Date().toISOString()
  };

  fs.writeFileSync(compactFile, JSON.stringify(compactContext, null, 2));

  // Inject context back (works for both pre and post)
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'PostCompact',
      additionalContext:
        `⛵ [Vela] 파이프라인 컨텍스트 복원:\n` +
        `  🧭 ${ptype} │ Step: ${step}\n` +
        `  Task: ${request}\n` +
        `  Completed: ${(state.completed_steps || []).join(' → ')}\n` +
        `  Artifact: ${artifactDir}\n` +
        `  이 파이프라인을 계속 진행하세요. node .vela/cli/vela-engine.js state 로 현재 상태를 확인하세요.`
    }
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
