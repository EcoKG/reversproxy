#!/usr/bin/env node
/**
 * ⛵ Vela TaskCompleted Hook — Quality gate before task completion
 *
 * When a task is marked as completed, verify that required artifacts exist.
 * Exit code 2 blocks completion and sends feedback.
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
  if (!state || !state._artifactDir) process.exit(0);

  const step = state.current_step;
  const artifactDir = state._artifactDir;

  // Check required artifacts for current step
  const checks = [];

  if (step === 'research') {
    if (!fs.existsSync(path.join(artifactDir, 'research.md')))
      checks.push('research.md가 아직 작성되지 않았습니다');
    // Verify teammate communication occurred (competing hypothesis)
    const commPath = path.join(velaDir, 'state', 'teammate-comms.json');
    try {
      if (fs.existsSync(commPath)) {
        const comms = JSON.parse(fs.readFileSync(commPath, 'utf-8'));
        const researchComms = comms.filter(c => c.step === 'research');
        if (researchComms.length === 0) {
          checks.push('경쟁가설 디버깅: Researcher 간 SendMessage 소통이 감지되지 않았습니다');
        }
      }
    } catch (e) {}
  }

  if (step === 'plan') {
    if (!fs.existsSync(path.join(artifactDir, 'plan.md')))
      checks.push('plan.md가 아직 작성되지 않았습니다');
  }

  if (step === 'execute') {
    if (!fs.existsSync(path.join(artifactDir, 'review-execute.md')))
      checks.push('review-execute.md가 아직 작성되지 않았습니다');
    if (!fs.existsSync(path.join(artifactDir, 'approval-execute.json')))
      checks.push('approval-execute.json이 아직 작성되지 않았습니다');
  }

  if (step === 'verify') {
    if (!fs.existsSync(path.join(artifactDir, 'verification.md')))
      checks.push('verification.md가 아직 작성되지 않았습니다');
  }

  if (checks.length > 0) {
    process.stderr.write(
      `⛵ [Vela] ✦ BLOCKED: 작업 완료 조건 미충족.\n` +
      `  Step: ${step}\n` +
      checks.map(c => `  ✗ ${c}`).join('\n')
    );
    process.exit(2);
  }

  process.exit(0);
}

main().catch(() => process.exit(0));
