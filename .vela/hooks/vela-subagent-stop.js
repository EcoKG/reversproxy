#!/usr/bin/env node
/**
 * ⛵ Vela SubagentStop Hook — Harvests subagent output and detects escalation
 *
 * When a subagent stops:
 * 1. Saves last_assistant_message to artifact directory
 * 2. Detects Reviewer score below threshold → creates escalation.json
 * 3. Cleans up delegation.json
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

  // K003: call once at top, reuse below
  const state = findActivePipeline(velaDir);
  if (!state) process.exit(0);

  // ─── Artifact harvest ───
  // Save subagent output to artifact directory for traceability
  const message = input.last_assistant_message;
  if (message && state._artifactDir) {
    const agentId = input.agent_id || 'unknown';
    const agentType = input.agent_type || '';
    const transcriptPath = input.agent_transcript_path || '';

    const header =
      `<!-- Vela SubagentStop artifact -->\n` +
      `<!-- agent_id: ${agentId} -->\n` +
      `<!-- agent_type: ${agentType} -->\n` +
      `<!-- transcript: ${transcriptPath} -->\n` +
      `<!-- harvested_at: ${new Date().toISOString()} -->\n\n`;

    const artifactPath = path.join(state._artifactDir, `subagent-${agentId}.md`);
    try {
      fs.writeFileSync(artifactPath, header + message);
    } catch (e) {
      // Skip artifact on filesystem error — non-critical
    }

    // ─── Escalation detection ───
    // Extract Reviewer score from output. Trigger escalation if below threshold.
    // Prefer explicit total pattern; fall back to bare N/25 pattern.
    // False negative > false positive: no score found → no escalation.
    const THRESHOLD = 15;
    const primaryRegex = /(?:총점|총|total\s*score)[^\d]*(\d+)\s*\/\s*25/i;
    const fallbackRegex = /\b(\d+)\s*\/\s*25\b/;

    const primaryMatch = message.match(primaryRegex);
    const fallbackMatch = message.match(fallbackRegex);
    const match = primaryMatch || fallbackMatch;

    if (match) {
      const score = parseInt(match[1], 10);
      if (score < THRESHOLD) {
        const stateDir = path.join(velaDir, 'state');
        try {
          if (!fs.existsSync(stateDir)) fs.mkdirSync(stateDir, { recursive: true });
          const escalationPath = path.join(stateDir, 'escalation.json');
          fs.writeFileSync(escalationPath, JSON.stringify({
            reason: 'reviewer_score_below_threshold',
            score,
            threshold: THRESHOLD,
            agent_id: agentId,
            timestamp: Date.now()
          }, null, 2));
        } catch (e) {
          // Skip escalation on filesystem error — non-critical
        }
      }
    }
  }

  // ─── Delegation cleanup ───
  const delegationPath = path.join(velaDir, 'state', 'delegation.json');
  if (fs.existsSync(delegationPath)) {
    try { fs.unlinkSync(delegationPath); } catch (e) {}
  }

  // ─── Output ───
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'SubagentStop'
    }
  }));

  process.exit(0);
}

main().catch(() => process.exit(0));
