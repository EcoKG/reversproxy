/**
 * Vela SDK Reviewer
 * 3-stage Haiku→Sonnet→Opus review module.
 * Calls runSdkAgent() for each stage — never imports SDK directly.
 *
 * Stage 1 (Haiku): Fast, cheap initial review. Score ≥ 20 → pass, < 15 → Stage 3 (Opus).
 * Borderline (15-19): Escalate to Stage 2 (Sonnet) for deeper analysis.
 * Stage 2 (Sonnet): Deep review. Score ≥ 20 → pass, < 20 → Stage 3 (Opus).
 * Stage 3 (Opus):  Final escalation. Score ≥ 20 → approve + escalated:true, < 20 → reject + escalated:true.
 *
 * Exports: sdkReview({ step, artifactDir, cwd })
 *
 * Design decisions:
 * - settingSources: [] passed through runSdkAgent (D014 — hook isolation)
 * - System prompt inlines reviewer instructions + scoring rubric as literal strings
 *   because SDK agents cannot read project files
 * - Score regex matches vela-subagent-stop.js patterns for consistency
 * - artifactDir and .vela/state/ assumed to exist (engine creates them)
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { runSdkAgent } = require('./sdk-runner');

// ─── Score regex — matches vela-subagent-stop.js patterns ───
const PRIMARY_SCORE_REGEX = /(총점|총|total\s*score)[^\d]*(\d+)\s*\/\s*25/i;
const FALLBACK_SCORE_REGEX = /\b(\d+)\s*\/\s*25\b/;

const PASS_THRESHOLD = 20;
const FAIL_THRESHOLD = 15;

// ─── Stage 3: Opus escalation model + budget ───
const OPUS_MODEL = 'claude-opus-4-20250514';
const OPUS_BUDGET = 0.50;

// ─── Inlined reviewer system prompt ───
// SDK agents run with settingSources: [] and cannot read project files.
// The entire reviewer context must be in the system prompt.
const REVIEWER_SYSTEM_PROMPT = `# Reviewer Agent

이 지시는 **절대적**이다. 예외 없이 따라야 한다.

## 역할
산출물을 독립적으로 평가한다. Worker의 추론 과정은 알 수 없다 — 산출물만 평가한다.
5개 차원 각 X/5, 총 X/25 점수를 매긴다.

## 채점 기준 — 5차원 모두 빠짐없이 평가한다

### 1. Layer Separation (X/5)
- Clean Architecture 레이어 분리
- 의존성 방향 (안쪽으로만)
- 도메인 레이어의 외부 의존성 없음

### 2. DDD Patterns (X/5)
- Aggregate Root 식별
- Entity/Value Object 구분
- Repository 인터페이스 위치 (도메인 레이어)
- 도메인 로직이 도메인 레이어에 있는지

### 3. SOLID Principles (X/5)
- SRP: 클래스당 하나의 변경 이유
- OCP: 확장 가능, 수정 불필요
- ISP: 적절한 크기의 인터페이스
- DIP: 추상에 의존, 구체에 의존하지 않음

### 4. Test Strategy (X/5)
- 테스트 케이스의 의미 (존재만이 아닌 실질적 검증)
- unit/integration/e2e 커버리지
- 엣지 케이스

### 5. Specification Completeness (X/5)
- 필요한 클래스/인터페이스 정의 완전성
- 메서드 시그니처 + 파라미터 + 반환 타입
- 누락된 중요 추상화

## 이슈 심각도
- **CRITICAL**: 근본적 설계 결함 — 반드시 수정 필요
- **HIGH**: 구현 전 수정 필요
- **MEDIUM**: 개선 권장
- **LOW**: 사소한 제안

## 절대 위반 금지
1. 산출물만 평가한다. 프로세스를 평가하지 않는다
2. 엄격하고 비판적으로 평가한다. 관대하게 점수를 주지 않는다
3. review-{step}.md만 작성한다. 소스 코드나 다른 산출물을 수정하지 않는다

## 출력 형식
반드시 마지막에 다음 형식으로 총점을 작성한다:
## Total: XX/25
`;

/**
 * Parse a review score from agent output text.
 * Returns the numeric score or null if not found.
 * @param {string} text - Agent result text
 * @returns {number|null}
 */
function parseScore(text) {
  if (!text || typeof text !== 'string') return null;

  const primaryMatch = text.match(PRIMARY_SCORE_REGEX);
  if (primaryMatch) return parseInt(primaryMatch[2], 10);

  const fallbackMatch = text.match(FALLBACK_SCORE_REGEX);
  if (fallbackMatch) return parseInt(fallbackMatch[1], 10);

  return null;
}

/**
 * Write review markdown artifact.
 * @param {string} artifactDir - Directory to write artifacts to
 * @param {string} step - Pipeline step name
 * @param {string} content - Review content from agent
 */
function writeReviewArtifact(artifactDir, step, content) {
  const filePath = path.join(artifactDir, `review-${step}.md`);
  fs.writeFileSync(filePath, content, 'utf8');
}

/**
 * Write approval JSON artifact.
 * @param {string} artifactDir - Directory to write artifacts to
 * @param {string} step - Pipeline step name
 * @param {Object} approval - Approval data
 */
function writeApprovalArtifact(artifactDir, step, approval) {
  const filePath = path.join(artifactDir, `approval-${step}.json`);
  fs.writeFileSync(filePath, JSON.stringify(approval, null, 2), 'utf8');
}

/**
 * Write escalation.json to .vela/state/.
 * @param {string} cwd - Project root directory
 * @param {number} score - Review score that triggered escalation
 * @param {Object} [extra] - Additional fields (e.g. auto_escalated)
 */
function writeEscalation(cwd, score, extra) {
  const stateDir = path.join(cwd, '.vela', 'state');
  const escalationPath = path.join(stateDir, 'escalation.json');
  fs.writeFileSync(escalationPath, JSON.stringify({
    reason: 'reviewer_score_below_threshold',
    score,
    threshold: FAIL_THRESHOLD,
    timestamp: new Date().toISOString(),
    ...(extra || {})
  }, null, 2), 'utf8');
}

/**
 * Build the review prompt for a given step.
 * @param {string} step - Pipeline step name
 * @param {string|null} priorReview - Prior stage review text (for Stage 2)
 * @returns {string}
 */
function buildReviewPrompt(step, priorReview) {
  let prompt = `다음 파이프라인 단계의 산출물을 리뷰하라: "${step}"\n\n`;
  prompt += `이 단계의 artifacts 디렉토리에 있는 모든 산출물을 읽고 5차원 채점 기준에 따라 평가하라.\n`;
  prompt += `반드시 마지막에 "## Total: XX/25" 형식으로 총점을 명시하라.\n`;

  if (priorReview) {
    prompt += `\n--- 이전 Haiku 리뷰 (참고용) ---\n${priorReview}\n--- 이전 리뷰 끝 ---\n`;
    prompt += `\n이전 리뷰를 참고하되, 독립적으로 재평가하라. 점수는 네 판단에 따라 달라질 수 있다.\n`;
  }

  return prompt;
}

/**
 * Run a single review stage via SDK agent.
 * @param {Object} opts
 * @param {string} opts.model - Model identifier
 * @param {string} opts.step - Pipeline step name
 * @param {string} opts.cwd - Working directory
 * @param {number} opts.maxTurns - Max conversation turns
 * @param {number} opts.maxBudgetUsd - Budget cap
 * @param {string|null} opts.priorReview - Prior review text (Stage 2 only)
 * @returns {Promise<Object>} { ok, result, score, cost, model, durationMs } or { ok: false, error }
 */
async function runReviewStage(opts) {
  const prompt = buildReviewPrompt(opts.step, opts.priorReview);

  const agentResult = await runSdkAgent({
    prompt,
    model: opts.model,
    cwd: opts.cwd,
    systemPrompt: REVIEWER_SYSTEM_PROMPT,
    maxTurns: opts.maxTurns,
    maxBudgetUsd: opts.maxBudgetUsd,
    // settingSources: [] is set by runSdkAgent internally (D014)
  });

  if (!agentResult.ok) {
    return {
      ok: false,
      error: agentResult.error,
      details: agentResult.details,
      cost: agentResult.cost || 0,
      durationMs: agentResult.durationMs || 0
    };
  }

  const resultText = agentResult.result || '';
  const score = parseScore(resultText);

  return {
    ok: true,
    result: resultText,
    score,
    cost: agentResult.cost || 0,
    model: agentResult.model || opts.model,
    durationMs: agentResult.durationMs || 0
  };
}

/**
 * Run Stage 3 Opus escalation.
 * Called when prior stages scored below PASS_THRESHOLD.
 *
 * @param {Object} opts
 * @param {string} opts.step - Pipeline step name
 * @param {string} opts.cwd - Working directory
 * @param {string} opts.priorReview - Best available prior review text
 * @returns {Promise<Object>} Same shape as runReviewStage
 */
async function runOpusEscalation({ step, cwd, priorReview }) {
  return runReviewStage({
    model: OPUS_MODEL,
    step,
    cwd,
    maxTurns: 10,
    maxBudgetUsd: OPUS_BUDGET,
    priorReview
  });
}

/**
 * Run 3-stage SDK review for a pipeline step.
 *
 * Stage 1: Haiku fast review
 *   - score ≥ 20 → approve
 *   - score < 15 → Stage 3 (Opus escalation)
 *   - 15-19 (borderline) → Stage 2
 *
 * Stage 2: Sonnet deep review
 *   - score ≥ 20 → approve
 *   - score < 20 → Stage 3 (Opus escalation)
 *
 * Stage 3: Opus escalation (auto)
 *   - score ≥ 20 → approve + escalated:true
 *   - score < 20 → reject + escalated:true
 *
 * @param {Object} opts
 * @param {string} opts.step - Pipeline step name (e.g. 'design', 'implement')
 * @param {string} opts.artifactDir - Directory for review artifacts
 * @param {string} opts.cwd - Project root working directory
 * @returns {Promise<Object>} Result:
 *   Success: { ok: true, score, decision: 'approve'|'reject', stage, model, cost, durationMs, escalated? }
 *   Failure: { ok: false, error: string }
 */
async function sdkReview({ step, artifactDir, cwd }) {
  const HAIKU_MODEL = 'claude-haiku-4-5-20250929';
  const SONNET_MODEL = 'claude-sonnet-4-5-20250929';

  let totalCost = 0;
  let totalDurationMs = 0;

  // ─── Stage 1: Haiku fast review ───
  const stage1 = await runReviewStage({
    model: HAIKU_MODEL,
    step,
    cwd,
    maxTurns: 5,
    maxBudgetUsd: 0.05,
    priorReview: null
  });

  if (!stage1.ok) {
    return { ok: false, error: stage1.error, details: stage1.details };
  }

  totalCost += stage1.cost;
  totalDurationMs += stage1.durationMs;
  const haikuScore = stage1.score;
  const haikuResult = stage1.result;

  // Score could not be parsed — treat as borderline to get Sonnet opinion
  if (haikuScore == null) {
    // Fall through to Stage 2 for a definitive answer
  } else if (haikuScore >= PASS_THRESHOLD) {
    // Clear pass — write artifacts, return
    writeReviewArtifact(artifactDir, step, haikuResult);
    writeApprovalArtifact(artifactDir, step, {
      decision: 'approve',
      score: haikuScore,
      threshold: PASS_THRESHOLD,
      stage: 'haiku',
      model: stage1.model,
      timestamp: new Date().toISOString()
    });

    return {
      ok: true,
      score: haikuScore,
      decision: 'approve',
      stage: 'haiku',
      model: stage1.model,
      cost: totalCost,
      durationMs: totalDurationMs
    };
  } else if (haikuScore < FAIL_THRESHOLD) {
    // ─── Stage 3: Opus escalation from clear Haiku fail ───
    const opusResult = await runOpusEscalation({ step, cwd, priorReview: haikuResult });
    totalCost += opusResult.cost || 0;
    totalDurationMs += opusResult.durationMs || 0;

    if (opusResult.ok && opusResult.score != null && opusResult.score >= PASS_THRESHOLD) {
      // Opus rescued it
      writeReviewArtifact(artifactDir, step, opusResult.result);
      writeApprovalArtifact(artifactDir, step, {
        decision: 'approve',
        score: opusResult.score,
        threshold: PASS_THRESHOLD,
        stage: 'opus',
        model: opusResult.model,
        escalated: true,
        escalation_model: 'opus',
        timestamp: new Date().toISOString()
      });

      return {
        ok: true,
        score: opusResult.score,
        decision: 'approve',
        stage: 'opus',
        model: opusResult.model,
        cost: totalCost,
        durationMs: totalDurationMs,
        escalated: true
      };
    }

    // Opus also failed (or errored) — reject with escalated flag
    const opusScore = (opusResult.ok && opusResult.score != null) ? opusResult.score : haikuScore;
    const opusReviewText = (opusResult.ok && opusResult.result) ? opusResult.result : haikuResult;

    writeReviewArtifact(artifactDir, step, opusReviewText);
    writeApprovalArtifact(artifactDir, step, {
      decision: 'reject',
      score: opusScore,
      threshold: PASS_THRESHOLD,
      stage: 'opus',
      model: opusResult.ok ? opusResult.model : OPUS_MODEL,
      escalated: true,
      escalation_model: 'opus',
      timestamp: new Date().toISOString()
    });
    writeEscalation(cwd, opusScore, { auto_escalated: true });

    return {
      ok: true,
      score: opusScore,
      decision: 'reject',
      stage: 'opus',
      model: opusResult.ok ? opusResult.model : OPUS_MODEL,
      cost: totalCost,
      durationMs: totalDurationMs,
      escalated: true
    };
  }

  // ─── Stage 2: Sonnet deep review (borderline 15-19 or unparseable score) ───
  const stage2 = await runReviewStage({
    model: SONNET_MODEL,
    step,
    cwd,
    maxTurns: 8,
    maxBudgetUsd: 0.15,
    priorReview: haikuResult
  });

  if (!stage2.ok) {
    // Stage 2 failed — still have Stage 1 result, report partial
    // Write Haiku artifacts as the best available review
    writeReviewArtifact(artifactDir, step, haikuResult);
    return {
      ok: false,
      error: stage2.error,
      details: `Stage 2 (Sonnet) failed: ${stage2.details || stage2.error}. Haiku score was ${haikuScore}.`,
      cost: totalCost + (stage2.cost || 0),
      durationMs: totalDurationMs + (stage2.durationMs || 0)
    };
  }

  totalCost += stage2.cost;
  totalDurationMs += stage2.durationMs;
  const sonnetScore = stage2.score;
  const sonnetResult = stage2.result;

  // Use Sonnet's score as the definitive assessment
  const finalScore = sonnetScore != null ? sonnetScore : haikuScore;

  if (finalScore != null && finalScore >= PASS_THRESHOLD) {
    // Sonnet approved — no escalation needed
    writeReviewArtifact(artifactDir, step, sonnetResult);
    writeApprovalArtifact(artifactDir, step, {
      decision: 'approve',
      score: finalScore,
      threshold: PASS_THRESHOLD,
      stage: 'sonnet',
      model: stage2.model,
      timestamp: new Date().toISOString()
    });

    return {
      ok: true,
      score: finalScore,
      decision: 'approve',
      stage: 'sonnet',
      model: stage2.model,
      cost: totalCost,
      durationMs: totalDurationMs
    };
  }

  // ─── Stage 3: Opus escalation from Sonnet fail ───
  const opusResult = await runOpusEscalation({ step, cwd, priorReview: sonnetResult });
  totalCost += opusResult.cost || 0;
  totalDurationMs += opusResult.durationMs || 0;

  if (opusResult.ok && opusResult.score != null && opusResult.score >= PASS_THRESHOLD) {
    // Opus rescued it after Sonnet failed
    writeReviewArtifact(artifactDir, step, opusResult.result);
    writeApprovalArtifact(artifactDir, step, {
      decision: 'approve',
      score: opusResult.score,
      threshold: PASS_THRESHOLD,
      stage: 'opus',
      model: opusResult.model,
      escalated: true,
      escalation_model: 'opus',
      timestamp: new Date().toISOString()
    });

    return {
      ok: true,
      score: opusResult.score,
      decision: 'approve',
      stage: 'opus',
      model: opusResult.model,
      cost: totalCost,
      durationMs: totalDurationMs,
      escalated: true
    };
  }

  // Opus also failed — final reject with escalated flag
  const opusScore = (opusResult.ok && opusResult.score != null) ? opusResult.score : finalScore;
  const opusReviewText = (opusResult.ok && opusResult.result) ? opusResult.result : sonnetResult;

  writeReviewArtifact(artifactDir, step, opusReviewText);
  writeApprovalArtifact(artifactDir, step, {
    decision: 'reject',
    score: opusScore,
    threshold: PASS_THRESHOLD,
    stage: 'opus',
    model: opusResult.ok ? opusResult.model : OPUS_MODEL,
    escalated: true,
    escalation_model: 'opus',
    timestamp: new Date().toISOString()
  });
  writeEscalation(cwd, opusScore, { auto_escalated: true });

  return {
    ok: true,
    score: opusScore,
    decision: 'reject',
    stage: 'opus',
    model: opusResult.ok ? opusResult.model : OPUS_MODEL,
    cost: totalCost,
    durationMs: totalDurationMs,
    escalated: true
  };
}

module.exports = { sdkReview };
