/**
 * Vela SDK Executor
 * Single-stage Sonnet execution module for pipeline execute steps.
 * Calls runSdkAgent() once — never imports SDK directly.
 *
 * The executor agent reads plan.md from the artifact directory,
 * implements code following TDD sub-phases (Red → Green → Refactor),
 * and writes task-summary.md when complete.
 *
 * Exports: sdkExecute({ step, artifactDir, cwd })
 *
 * Design decisions:
 * - settingSources: [] passed through runSdkAgent (D014 — hook isolation)
 * - System prompt inlines executor instructions + TDD procedure as literal strings
 *   because SDK agents cannot read project files
 * - readwrite tools required — executor modifies source code
 * - Budget $1.00 / maxTurns 25 — implementation is heavier than review
 * - artifactDir and .vela/state/ assumed to exist (engine creates them)
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { runSdkAgent } = require('./sdk-runner');

// ─── Constants ───
const SONNET_MODEL = 'claude-sonnet-4-6-20250514';
const MAX_TURNS = 25;
const MAX_BUDGET_USD = 1.00;

// ─── Inlined executor system prompt ───
// SDK agents run with settingSources: [] and cannot read project files.
// The entire executor context must be in the system prompt.
// Content sourced from: scripts/agents/executor.md + scripts/agents/executor/tdd.md
const EXECUTOR_SYSTEM_PROMPT = `# Vela-Executor Agent

> Model: Sonnet | Mode: ReadWrite | Output: 코드 구현

## 역할 개요

plan.md의 Class Specification에 따라 코드를 구현하는 실행자.
plan.md를 반드시 먼저 읽고, 명세에 맞게 구현한다.
TDD 순서(test → implement → refactor)를 따른다.

규칙:
- plan.md가 설계도. 명세를 벗어나지 않는다
- \`.vela/\` 내부는 아티팩트 디렉토리만 쓰기 가능
- Reviewer가 plan.md 대비 코드를 비교 평가한다

---

## TDD Sub-Phases — 순서를 절대 건너뛰지 않는다

### Phase 1: test-write (Red)
plan.md의 Test Strategy에 따라 테스트를 작성한다.
테스트를 실행하여 **Red 상태를 확인**한 후 다음 단계로 진행한다.

### Phase 2: implement (Green)
모든 테스트를 통과하는 구현 코드를 작성한다.
Class Specification을 **정확히** 따른다.
구현 후 테스트를 실행하여 **Green 상태를 확인**한다.

### Phase 3: refactor (Refactor)
동작을 변경하지 않고 코드 구조를 정리한다.
Architecture 섹션의 레이어 구조에 맞춘다.
리팩토링 후 테스트를 재실행하여 **Green 유지를 확인**한다.

### 테스트 실행 — 반드시 실행하여 확인
프로젝트의 테스트 러너를 파악하여 실행:
- Node: \`npm test\` / \`npx jest\` / \`npx vitest\`
- Java: \`mvn test\` / \`gradle test\`
- Python: \`pytest\`
- Go: \`go test ./...\`

---

## 파일 소유권

Teammate로 소환된 경우, 프롬프트에 **담당 파일**이 명시된다.
담당 파일만 수정하고, 다른 팀원의 파일은 읽기만 한다.

다른 팀원의 파일에 변경이 필요하면:
- 해당 팀원에게 SendMessage로 요청
- 직접 수정하지 않는다

---

## Git Worktree

\`isolation: "worktree"\`로 소환된 경우:
- 격리된 git worktree에서 작업 중
- 다른 팀원과 파일 시스템이 분리됨
- 작업 완료 후 Conflict Manager가 병합

---

## Communication

**Subagent로 소환된 경우:**
- 완료 시: "Implementation complete. All tests passing."

**Teammate로 소환된 경우:**
- 완료 시 PM에게 SendMessage
- 다른 팀원과 인터페이스 조율 시 SendMessage 활용

---

## task-summary.md 작성

구현 완료 후 아티팩트 디렉토리에 task-summary.md를 반드시 작성한다.
다음 내용을 포함:
- 구현한 파일 목록
- 통과한 테스트 요약
- plan.md 대비 변경 사항 (있을 경우)
- 발견된 이슈 (있을 경우)
`;

/**
 * Write task-summary.md artifact to the artifact directory.
 * @param {string} artifactDir - Directory to write artifacts to
 * @param {string} content - Summary content from agent
 */
function writeTaskSummaryArtifact(artifactDir, content) {
  const filePath = path.join(artifactDir, 'task-summary.md');
  fs.writeFileSync(filePath, content, 'utf8');
}

/**
 * Run SDK executor for a pipeline execute step.
 *
 * Single-stage Sonnet execution:
 * 1. Agent reads plan.md from artifactDir
 * 2. Follows TDD sub-phases to implement code
 * 3. Writes task-summary.md when complete
 *
 * @param {Object} opts
 * @param {string} opts.step - Pipeline step name (e.g. 'execute', 'implement')
 * @param {string} opts.artifactDir - Directory containing plan.md, receives task-summary.md
 * @param {string} opts.cwd - Project root working directory
 * @returns {Promise<Object>} Result:
 *   Success: { ok: true, step, artifact: 'task-summary.md', cost, model, numTurns, durationMs }
 *   SDK unavailable: { ok: false, error: 'sdk_not_available' }
 *   Failure: { ok: false, error, details, cost?, numTurns?, durationMs? }
 */
async function sdkExecute({ step, artifactDir, cwd }) {
  // ─── Build user prompt ───
  const prompt = [
    `파이프라인 단계 "${step}"의 구현을 수행하라.`,
    '',
    '## 지시사항',
    '',
    `1. 먼저 아티팩트 디렉토리의 plan.md를 읽어라: ${artifactDir}/plan.md`,
    '2. plan.md의 Class Specification에 따라 TDD 순서로 구현하라:',
    '   - Phase 1 (Red): Test Strategy에 따라 테스트 작성 → 실행하여 실패 확인',
    '   - Phase 2 (Green): 테스트 통과하는 구현 작성 → 실행하여 통과 확인',
    '   - Phase 3 (Refactor): 코드 정리 → 테스트 재실행하여 통과 유지 확인',
    '3. Class Specification을 정확히 따르라. 명세를 벗어나지 않는다.',
    `4. .vela/ 내부는 아티팩트 디렉토리(${artifactDir})만 쓰기 가능하다.`,
    `5. 완료 후 ${artifactDir}/task-summary.md를 작성하라.`,
    '',
    '## 중요',
    '- plan.md가 설계도이다. 임의로 구조를 변경하지 않는다.',
    '- 테스트를 반드시 실행하여 결과를 확인한다.',
    '- task-summary.md에 구현 파일 목록, 테스트 결과, 변경사항을 기록한다.',
  ].join('\n');

  // ─── Call Sonnet via SDK ───
  const agentResult = await runSdkAgent({
    prompt,
    model: SONNET_MODEL,
    cwd,
    systemPrompt: EXECUTOR_SYSTEM_PROMPT,
    maxTurns: MAX_TURNS,
    maxBudgetUsd: MAX_BUDGET_USD,
    permissionMode: 'bypassPermissions',
  });

  // ─── SDK unavailable — return without writing artifacts ───
  if (agentResult.error === 'sdk_not_available') {
    return { ok: false, error: 'sdk_not_available' };
  }

  // ─── SDK error — return error details ───
  if (!agentResult.ok) {
    return {
      ok: false,
      error: agentResult.error,
      details: agentResult.details,
      cost: agentResult.cost || 0,
      numTurns: agentResult.numTurns,
      durationMs: agentResult.durationMs || 0,
    };
  }

  // ─── Success — write task-summary.md if agent didn't already ───
  const summaryPath = path.join(artifactDir, 'task-summary.md');
  if (!fs.existsSync(summaryPath)) {
    // Agent should have written it, but as a safety net,
    // write the result text as the summary
    const fallbackContent = [
      '# Task Summary',
      '',
      `- **Step:** ${step}`,
      `- **Model:** ${agentResult.model || SONNET_MODEL}`,
      `- **Cost:** $${(agentResult.cost || 0).toFixed(4)}`,
      `- **Turns:** ${agentResult.numTurns || 'N/A'}`,
      `- **Duration:** ${agentResult.durationMs || 0}ms`,
      `- **Timestamp:** ${new Date().toISOString()}`,
      '',
      '---',
      '',
      agentResult.result || '(no result text)',
    ].join('\n');

    writeTaskSummaryArtifact(artifactDir, fallbackContent);
  }

  return {
    ok: true,
    step,
    artifact: 'task-summary.md',
    cost: agentResult.cost || 0,
    model: agentResult.model || SONNET_MODEL,
    numTurns: agentResult.numTurns,
    durationMs: agentResult.durationMs || 0,
  };
}

module.exports = { sdkExecute };
