#!/usr/bin/env node
/**
 * ⛵ Vela Stop Hook — Blocks premature session end during active pipeline
 *
 * Fires on the Stop event.
 * - stop_hook_active=true → immediate pass (prevents infinite loop)
 * - auto=true + incomplete pipeline → decision:block (physically prevents stop)
 * - auto=false + active pipeline → systemMessage warning (informational)
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

  // ─── Infinite-loop guard (must be first) ───
  if (input.stop_hook_active === true) process.exit(0);

  const cwd = input.cwd || process.cwd();
  const velaDir = path.join(cwd, '.vela');
  const config = readConfig(cwd);
  if (!config) process.exit(0);

  // K003: call once at top, reuse below
  const state = findActivePipeline(velaDir);
  if (!state) process.exit(0);

  // ─── Clean up delegation signal ───
  const delegationPath = path.join(velaDir, 'state', 'delegation.json');
  if (fs.existsSync(delegationPath)) {
    try { fs.unlinkSync(delegationPath); } catch (e) {}
  }

  // ─── Build diagnostic fields ───
  const step = state.current_step || '?';
  const ptype = state.pipeline_type || '?';
  const request = (state.request || '').substring(0, 50);
  const completedSteps = Array.isArray(state.completed_steps) ? state.completed_steps : [];
  const totalSteps = state.total_steps || '?';
  const remaining = typeof totalSteps === 'number'
    ? totalSteps - completedSteps.length
    : '?';

  if (state.auto === true) {
    // ─── Auto mode: physically block the stop ───
    process.stdout.write(JSON.stringify({
      decision: 'block',
      reason: `⛵ [Vela] 파이프라인 미완료 — 중단 차단\n` +
        `  현재 단계: ${step} │ 남은 단계: ${remaining}\n` +
        `  다음 행동: transition을 호출하여 파이프라인을 계속 진행하세요.`
    }));
    process.exit(0);
  }

  // ─── Non-auto mode: warn user ───
  process.stdout.write(JSON.stringify({
    systemMessage: `⛵ [Vela] 활성 파이프라인이 있습니다!\n` +
      `  🧭 ${ptype} │ Step: ${step} │ ${request}\n` +
      `  다음 세션에서 /vela 로 재개할 수 있습니다.`
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
