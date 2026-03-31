/**
 * Vela SDK Researcher
 * 3-perspective parallel research module using Haiku agents.
 * Runs architecture, security, quality analysis concurrently via Promise.allSettled()
 * and merges results into research.md.
 *
 * Perspectives:
 * - Architecture: layer separation, dependency direction, coupling, tech debt
 * - Security: auth vulnerabilities, input validation, credential exposure, CVEs
 * - Quality: test coverage, error handling, performance bottlenecks, code duplication
 *
 * Each perspective uses the competing-hypothesis debugging procedure (hypothesis.md)
 * as a prefix to enforce structured analysis.
 *
 * Exports: sdkResearch({ step, artifactDir, cwd })
 *
 * Design decisions:
 * - settingSources: [] passed through runSdkAgent (D014 — hook isolation)
 * - All perspective prompts inlined as constants because SDK agents cannot read project files
 * - Promise.allSettled() — partial failures still produce research.md with available results
 * - research.md always written, even if all 3 perspectives fail
 * - Hypothesis prefix prepended to each perspective prompt for structured reasoning
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { runSdkAgent } = require('./sdk-runner');

// ─── Constants ───
const HAIKU_MODEL = 'claude-haiku-4-5-20250929';
const MAX_TURNS = 5;
const MAX_BUDGET_USD = 0.05;

// ─── Hypothesis prefix — prepended to all perspective prompts ───
// Enforces competing-hypothesis debugging procedure for structured analysis.
// Source: scripts/agents/researcher/hypothesis.md (inlined for SDK isolation)
const HYPOTHESIS_PREFIX = `# 경쟁가설 디버깅 — 반드시 이 절차를 따른다

## 절차 — 단계를 건너뛰지 않는다
1. **가설 생성** — 문제/작업에 대해 3~5개의 경쟁 가설을 수립한다
2. **증거 수집** — 각 가설에 대한 지지/반박 증거를 코드에서 수집한다
3. **가설 제거** — 증거와 모순되는 가설을 신속히 제거한다
4. **결론** — 최종 생존 가설과 근거를 research.md에 문서화한다

## 원칙
- 반박 증거가 나오면 **즉시** 해당 가설을 탈락시킨다. 방어하지 않는다
- 모든 가설에 동일한 시간을 쓰지 않는다. 증거 기반으로 신속히 좁힌다
- 탈락 가설도 research.md에 간략히 기록한다 (왜 제거되었는지)
- 디테일하되 과하지 않게. 토큰 낭비 금지
- 단독 분석이므로 자체적으로 모든 관점에서 반박 증거를 검증한다

`;

// ─── Perspective system prompts ───
// Each combines HYPOTHESIS_PREFIX + perspective-specific instructions.
// [PERSPECTIVE:xxx] marker at start enables test mock differentiation.

const ARCHITECTURE_SYSTEM_PROMPT = `[PERSPECTIVE:architecture]

${HYPOTHESIS_PREFIX}# 아키텍처 관점 분석 가이드

architecture-researcher로 소환된 경우 이 가이드를 **반드시** 따른다.

## 분석 대상
- 레이어 분리 (도메인, 애플리케이션, 인프라, 인터페이스)
- 의존성 방향 (안쪽으로만 흘러야 함)
- 순환 참조
- 모듈 결합도/응집도
- 확장성, 유지보수성
- 기존 패턴과의 일관성
- 기술 부채 (TODO, FIXME, 임시 코드)

## 가설 예시
- H1: 도메인 레이어가 인프라에 직접 의존하고 있을 수 있음
- H2: 비즈니스 로직이 컨트롤러에 누출되어 있을 수 있음
- H3: 모듈 간 순환 참조가 존재할 수 있음

## 출력
분석 결과를 마크다운으로 출력한다.
가설, 증거, 결론을 구조적으로 정리한다.
`;

const SECURITY_SYSTEM_PROMPT = `[PERSPECTIVE:security]

${HYPOTHESIS_PREFIX}# 보안 관점 분석 가이드

security-researcher로 소환된 경우 이 가이드를 **반드시** 따른다.

## 분석 대상
- 인증/인가 취약점 (미들웨어 바이패스, 권한 상승)
- 입력 검증 (SQL injection, XSS, CSRF, path traversal)
- 비밀키/자격증명 하드코딩
- 불안전한 의존성 (알려진 CVE)
- 데이터 노출 (로깅에 민감정보, 에러 메시지 정보 유출)
- 암호화 (평문 저장, 약한 해시)

## 가설 예시
- H1: 인증 미들웨어가 특정 라우트를 건너뛰고 있을 수 있음
- H2: 세션 토큰 만료 검증이 없을 수 있음
- H3: SQL 쿼리에 사용자 입력이 직접 삽입될 수 있음

## 출력
분석 결과를 마크다운으로 출력한다.
가설, 증거, 결론을 구조적으로 정리한다.
`;

const QUALITY_SYSTEM_PROMPT = `[PERSPECTIVE:quality]

${HYPOTHESIS_PREFIX}# 품질/성능 관점 분석 가이드

quality-researcher로 소환된 경우 이 가이드를 **반드시** 따른다.

## 분석 대상
- 테스트 커버리지, 엣지 케이스 누락
- 에러 처리 (try-catch 누락, 에러 삼킴)
- 성능 병목 (N+1 쿼리, 불필요한 루프, 메모리 누수)
- 코드 중복 (DRY 위반)
- 가독성, 네이밍 컨벤션
- 불필요한 연산, 미사용 코드

## 가설 예시
- H1: 데이터베이스 쿼리에서 N+1 문제가 발생하고 있을 수 있음
- H2: 에러 핸들링이 일관되지 않아 예외가 삼켜지고 있을 수 있음
- H3: 동일 로직이 여러 파일에 중복되어 있을 수 있음

## 출력
분석 결과를 마크다운으로 출력한다.
가설, 증거, 결론을 구조적으로 정리한다.
`;

// ─── Perspectives array for iteration ───
const PERSPECTIVES = [
  { key: 'architecture', prompt: ARCHITECTURE_SYSTEM_PROMPT },
  { key: 'security', prompt: SECURITY_SYSTEM_PROMPT },
  { key: 'quality', prompt: QUALITY_SYSTEM_PROMPT },
];

/**
 * Write research.md artifact.
 * Always writes — even if all perspectives fail.
 * @param {string} artifactDir - Directory to write artifacts to
 * @param {string} content - Research result content
 */
function writeResearchArtifact(artifactDir, content) {
  const filePath = path.join(artifactDir, 'research.md');
  fs.writeFileSync(filePath, content, 'utf8');
}

/**
 * Run 3-perspective parallel SDK research analysis.
 *
 * Launches architecture, security, quality Haiku agents concurrently,
 * collects results via Promise.allSettled(), merges into research.md.
 *
 * @param {Object} opts
 * @param {Object} opts.step - Current pipeline step context (name, description, etc.)
 * @param {string} opts.artifactDir - Directory to write research.md
 * @param {string} opts.cwd - Project root working directory
 * @returns {Promise<Object>} Result:
 *   { ok, perspectives: [{key, ok, result?, error?, cost, durationMs}], totalCost, totalDurationMs }
 *   ok is true when at least one perspective succeeded.
 */
async function sdkResearch({ step, artifactDir, cwd }) {
  const overallStart = Date.now();

  // ─── Build user prompt with step context ───
  const stepContext = step
    ? `현재 단계: ${step.name || step}\n설명: ${step.description || ''}\n\n`
    : '';

  // ─── Launch 3 perspectives in parallel ───
  const agentPromises = PERSPECTIVES.map(({ key, prompt }) => {
    const userPrompt = `${stepContext}프로젝트 코드를 ${key} 관점에서 분석하라.\n\n경쟁가설 절차를 따르되, ${key} 분석 대상에 집중한다.\n코드베이스를 탐색하여 증거를 수집하고, 가설을 검증/탈락시켜라.`;

    return runSdkAgent({
      prompt: userPrompt,
      model: HAIKU_MODEL,
      cwd,
      systemPrompt: prompt,
      maxTurns: MAX_TURNS,
      maxBudgetUsd: MAX_BUDGET_USD,
    });
  });

  const settled = await Promise.allSettled(agentPromises);

  // ─── Collect results per perspective ───
  const perspectiveResults = PERSPECTIVES.map(({ key }, idx) => {
    const outcome = settled[idx];

    if (outcome.status === 'rejected') {
      return {
        key,
        ok: false,
        error: outcome.reason?.message || String(outcome.reason),
        cost: 0,
        durationMs: 0,
      };
    }

    const agentResult = outcome.value;
    if (!agentResult.ok) {
      return {
        key,
        ok: false,
        error: agentResult.error,
        details: agentResult.details,
        cost: agentResult.cost || 0,
        durationMs: agentResult.durationMs || 0,
      };
    }

    return {
      key,
      ok: true,
      result: agentResult.result,
      cost: agentResult.cost || 0,
      durationMs: agentResult.durationMs || 0,
    };
  });

  // ─── Compute totals ───
  const totalCost = perspectiveResults.reduce((sum, p) => sum + p.cost, 0);
  const totalDurationMs = Date.now() - overallStart;
  const anyOk = perspectiveResults.some(p => p.ok);

  // ─── Build research.md content ───
  const sections = perspectiveResults.map(p => {
    const header = `## ${p.key.charAt(0).toUpperCase() + p.key.slice(1)} 관점`;

    if (p.ok) {
      return [
        header,
        '',
        `> Cost: $${p.cost.toFixed(4)} | Duration: ${p.durationMs}ms`,
        '',
        p.result,
      ].join('\n');
    }

    return [
      header,
      '',
      `> ⚠️ 분석 실패`,
      '',
      `- **Error:** ${p.error}`,
      p.details ? `- **Details:** ${p.details}` : null,
      `- **Cost:** $${p.cost.toFixed(4)}`,
      `- **Duration:** ${p.durationMs}ms`,
    ].filter(Boolean).join('\n');
  });

  const researchContent = [
    '# Research — 3관점 병렬 분석',
    '',
    `- **Timestamp:** ${new Date().toISOString()}`,
    `- **Total Cost:** $${totalCost.toFixed(4)}`,
    `- **Total Duration:** ${totalDurationMs}ms`,
    `- **Perspectives OK:** ${perspectiveResults.filter(p => p.ok).length}/3`,
    '',
    '---',
    '',
    ...sections,
  ].join('\n');

  // ─── Always write research.md ───
  writeResearchArtifact(artifactDir, researchContent);

  return {
    ok: anyOk,
    perspectives: perspectiveResults,
    totalCost,
    totalDurationMs,
  };
}

module.exports = { sdkResearch };
