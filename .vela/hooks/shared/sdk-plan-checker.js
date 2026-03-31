/**
 * Vela SDK Plan Checker
 * Single-stage Haiku plan.md structural verification.
 * Calls runSdkAgent() to verify plan.md has required sections with substance.
 *
 * Checks:
 * - Three required sections: ## Architecture, ## Class Specification, ## Test Strategy
 * - Each section has ≥200 bytes of content after the header
 * - Internal consistency between sections
 *
 * Exports: sdkPlanCheck({ artifactDir, cwd })
 *
 * Design decisions:
 * - settingSources: [] passed through runSdkAgent (D014 — hook isolation)
 * - System prompt inlines all verification rules as literal strings
 *   because SDK agents cannot read project files
 * - plan-check.md written even on SDK failure — exit gate checks file existence
 * - Single Haiku call with low budget ($0.03) — structural check, not deep review
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { runSdkAgent } = require('./sdk-runner');

// ─── Constants ───
const HAIKU_MODEL = 'claude-haiku-4-5-20250929';
const MAX_TURNS = 3;
const MAX_BUDGET_USD = 0.03;

const VERDICT_REGEX = /VERDICT:\s*(PASS|FAIL)/i;

// ─── Self-contained system prompt ───
// SDK agents run with settingSources: [] and cannot read project files.
// The entire verification context must be in the system prompt.
const PLAN_CHECK_SYSTEM_PROMPT = `# Plan.md Structure Verifier

You are a structural verification agent for software design documents (plan.md).
Your job is to check that the document meets minimum structural requirements.

## Required Sections

The plan.md MUST contain ALL three of these sections (exact markdown headers):

1. \`## Architecture\` — Layer structure, dependency direction, module separation, directory layout
2. \`## Class Specification\` — Interfaces with method signatures + return types, classes with constructor params + methods, Value Objects, Aggregate Roots
3. \`## Test Strategy\` — Concrete test case names and descriptions, unit/integration/e2e coverage, edge cases

## Verification Rules

For EACH required section:
1. The exact header must exist (e.g. \`## Architecture\`)
2. The content after the header (until the next ## header or end of document) must have at least 200 bytes of substantive content (not just whitespace or placeholder text)
3. The content must be relevant to the section topic (not filler)

## Consistency Check

Verify that:
- Classes/interfaces in Class Specification are referenced in Architecture
- Test cases in Test Strategy correspond to classes/interfaces in Class Specification
- No major contradictions between sections

## Output Format

For each section, report:
- Whether the header exists
- Byte count of content
- Whether content is substantive
- Any issues found

Then output your final verdict on its own line:
\`VERDICT: PASS\` — all three sections exist with ≥200 bytes of substantive content
\`VERDICT: FAIL\` — one or more sections missing, too short, or containing only placeholder text

The VERDICT line must appear exactly as shown, on its own line.
`;

/**
 * Write plan-check.md artifact.
 * Always writes — exit gate (plan_check_pass) checks file existence.
 * @param {string} artifactDir - Directory to write artifacts to
 * @param {string} content - Check result content
 */
function writePlanCheckArtifact(artifactDir, content) {
  const filePath = path.join(artifactDir, 'plan-check.md');
  fs.writeFileSync(filePath, content, 'utf8');
}

/**
 * Run SDK plan structure check for plan.md.
 *
 * Reads plan.md from artifactDir, sends to Haiku for structural verification,
 * writes plan-check.md with results.
 *
 * @param {Object} opts
 * @param {string} opts.artifactDir - Directory containing plan.md
 * @param {string} opts.cwd - Project root working directory
 * @returns {Promise<Object>} Result:
 *   Success: { ok: true, verdict: 'pass'|'fail', result, cost, model, durationMs }
 *   plan.md missing: { ok: false, error: 'plan_md_not_found' }
 *   SDK failure: { ok: false, error, details, cost?, durationMs? }
 */
async function sdkPlanCheck({ artifactDir, cwd }) {
  // ─── Check plan.md exists ───
  const planPath = path.join(artifactDir, 'plan.md');
  if (!fs.existsSync(planPath)) {
    return { ok: false, error: 'plan_md_not_found' };
  }

  const planContent = fs.readFileSync(planPath, 'utf8');

  // ─── Call Haiku via SDK ───
  const agentResult = await runSdkAgent({
    prompt: `다음 plan.md의 구조를 검증하라:\n\n---\n${planContent}\n---\n\n위 검증 규칙에 따라 각 섹션을 확인하고 VERDICT를 출력하라.`,
    model: HAIKU_MODEL,
    cwd,
    systemPrompt: PLAN_CHECK_SYSTEM_PROMPT,
    maxTurns: MAX_TURNS,
    maxBudgetUsd: MAX_BUDGET_USD,
  });

  // ─── Handle SDK failure — still write artifact ───
  if (!agentResult.ok) {
    const errorContent = [
      '# Plan Check — Error',
      '',
      `SDK agent returned an error.`,
      '',
      `- **Error:** ${agentResult.error}`,
      `- **Details:** ${agentResult.details || 'N/A'}`,
      `- **Timestamp:** ${new Date().toISOString()}`,
    ].join('\n');

    writePlanCheckArtifact(artifactDir, errorContent);

    return {
      ok: false,
      error: agentResult.error,
      details: agentResult.details,
      cost: agentResult.cost || 0,
      durationMs: agentResult.durationMs || 0,
    };
  }

  // ─── Parse verdict from result ───
  const resultText = agentResult.result || '';
  const verdictMatch = resultText.match(VERDICT_REGEX);
  const verdict = verdictMatch ? verdictMatch[1].toLowerCase() : 'fail';

  // ─── Write plan-check.md ───
  const checkContent = [
    '# Plan Check Results',
    '',
    `- **Verdict:** ${verdict.toUpperCase()}`,
    `- **Model:** ${agentResult.model || HAIKU_MODEL}`,
    `- **Cost:** $${(agentResult.cost || 0).toFixed(4)}`,
    `- **Duration:** ${agentResult.durationMs || 0}ms`,
    `- **Timestamp:** ${new Date().toISOString()}`,
    '',
    '---',
    '',
    resultText,
  ].join('\n');

  writePlanCheckArtifact(artifactDir, checkContent);

  return {
    ok: true,
    verdict,
    result: resultText,
    cost: agentResult.cost || 0,
    model: agentResult.model || HAIKU_MODEL,
    durationMs: agentResult.durationMs || 0,
  };
}

module.exports = { sdkPlanCheck };
