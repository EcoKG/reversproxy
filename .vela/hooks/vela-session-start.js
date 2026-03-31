#!/usr/bin/env node
/**
 * ⛵ Vela SessionStart Hook — Detects interrupted pipelines on session start
 *
 * Fires when a new session begins. If an active pipeline exists,
 * injects context so Claude knows to offer resume.
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

  const step = state.current_step || '?';
  const ptype = state.pipeline_type || '?';
  const request = (state.request || '').substring(0, 60);
  const updated = state.updated_at || '';

  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'SessionStart',
      additionalContext:
        `⛵ [Vela] 이전 세션에서 중단된 파이프라인이 있습니다.\n` +
        `  🧭 ${ptype} │ Step: ${step} │ ${request}\n` +
        `  마지막 업데이트: ${updated}\n` +
        `  사용자에게 AskUserQuestion으로 물어보세요:\n` +
        `  "이전 파이프라인을 재개할까요?" → 재개 / 취소하고 새로 시작 / 무시`
    }
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
