#!/usr/bin/env node
/**
 * ⛵ Vela Notification Hook — Desktop notification relay
 *
 * Fires on Notification events from Claude Code.
 * Sends OS-level desktop notifications via platform-native tools:
 *   - macOS (darwin): osascript display notification
 *   - Linux: notify-send (if available)
 *   - Other platforms: silent no-op
 *
 * Self-contained — no pipeline.js or config dependency.
 * Notification failures are swallowed — this hook NEVER affects exit code.
 * Always exits 0 — Notification hooks have no decision control.
 *
 * Input format (stdin JSON):
 * {
 *   "session_id": "abc123",
 *   "cwd": "/Users/...",
 *   "hook_event_name": "Notification",
 *   "message": "Claude needs your permission to use Bash",
 *   "title": "Permission needed",
 *   "notification_type": "permission_prompt"
 * }
 *
 * Notification types: permission_prompt, idle_prompt, auth_success, elicitation_dialog
 */

const { execSync } = require('child_process');

async function main() {
  // ─── Parse stdin JSON ───
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    const raw = Buffer.concat(chunks).toString().trim();
    if (!raw) process.exit(0);
    input = JSON.parse(raw);
  } catch {
    // Malformed or missing stdin — exit silently
    process.exit(0);
  }

  // ─── Extract fields with sensible defaults ───
  const title = String(input.title || 'Vela Notification').replace(/"/g, '\\"');
  const message = String(input.message || '').replace(/"/g, '\\"');
  if (!message) process.exit(0);

  // ─── Platform-specific notification dispatch ───
  const platform = process.platform;

  try {
    if (platform === 'darwin') {
      // macOS: osascript display notification
      execSync(
        `osascript -e 'display notification "${message}" with title "${title}"'`,
        { stdio: 'ignore', timeout: 5000 }
      );
    } else if (platform === 'linux') {
      // Linux: notify-send (freedesktop.org notification)
      execSync(
        `notify-send "${title}" "${message}"`,
        { stdio: 'ignore', timeout: 5000 }
      );
    }
    // Other platforms: silent no-op
  } catch {
    // Notification failure must NEVER affect exit code
    // notify-send may not be installed, osascript may fail — all swallowed
  }

  process.exit(0);
}

main().catch(() => process.exit(0));
