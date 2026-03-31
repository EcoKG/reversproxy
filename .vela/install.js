#!/usr/bin/env node
/**
 * Vela Hook Installer
 *
 * Registers Vela hooks into the PROJECT-LOCAL .claude/settings.local.json
 * so they only trigger within this Vela-enabled project.
 *
 * Why project-local instead of global (~/.claude/settings.json)?
 * - Vela is a sandbox — hooks should not leak outside the project
 * - No performance overhead on non-Vela projects
 * - Multiple Vela projects can have independent configurations
 * - Deleting the project automatically removes hook registrations
 *
 * Usage:
 *   node install.js                    — Install hooks
 *   node install.js verify             — Verify installation
 *   node install.js upgrade            — Update all Vela files to latest version
 *   node install.js uninstall          — Remove all Vela hooks
 *   node install.js status             — Show current hook status
 */

const fs = require('fs');
const path = require('path');

const HOME = process.env.HOME || process.env.USERPROFILE;
const PROJECT_ROOT = findProjectRoot(process.cwd());
const SETTINGS_PATH = path.join(PROJECT_ROOT, '.claude', 'settings.local.json');
const VELA_HOOKS_DIR = path.join(PROJECT_ROOT, '.vela', 'hooks');

/**
 * Walk up from cwd to find the project root (where .vela/ lives).
 */
function findProjectRoot(startDir) {
  let dir = startDir;
  while (dir !== path.dirname(dir)) {
    if (fs.existsSync(path.join(dir, '.vela'))) {
      return dir;
    }
    dir = path.dirname(dir);
  }
  return startDir;
}

const HOOK_PREFIX = 'vela-';

// ─── Permission deny rules ───
// Claude Code's deny rules are absolute — denied at any level = blocked everywhere.
// These rules provide a second layer of defense alongside Gate Keeper/Guard hooks.
const VELA_PERMISSIONS = {
  deny: [
    // Destructive file operations
    'Bash(rm -rf *)',
    'Bash(rm -r *)',
    // Force push — all variants
    'Bash(git push --force *)',
    'Bash(git push -f *)',
    'Bash(git push --force-with-lease *)',
    'Bash(git push origin +*)',
    // Hard reset — destroys uncommitted work
    'Bash(git reset --hard *)',
    // Skip hooks — Vela hooks must never be bypassed
    'Bash(git commit --no-verify *)',
    'Bash(git commit -n *)',
    // Clean untracked files — can delete work
    'Bash(git clean -f *)',
    'Bash(git clean -fd *)',
    // Direct database drops
    'Bash(drop database *)',
    'Bash(DROP DATABASE *)',
  ],
  allow: [
    // Vela CLI tools — always allowed through Bash
    'Bash(node .vela/*)',
    'Bash(python .vela/*)',
    'Bash(python3 .vela/*)',
  ]
};

const VELA_HOOKS = [
  {
    matcher: 'PreToolUse',
    hookId: 'vela-gate-keeper',
    script: 'vela-gate-keeper.js',
    description: '⛵ Checking harbor clearance...'
  },
  {
    matcher: 'PreToolUse',
    hookId: 'vela-gate-guard',
    script: 'vela-gate-guard.js',
    description: '🌟 Verifying navigation chart...'
  },
  {
    matcher: 'UserPromptSubmit',
    hookId: 'vela-orchestrator',
    script: 'vela-orchestrator.js',
    description: '🧭 Plotting current position...'
  },
  {
    matcher: 'PostToolUse',
    hookId: 'vela-tracker',
    script: 'vela-tracker.js',
    description: '🔭 Logging voyage data...'
  },
  {
    matcher: 'Stop',
    hookId: 'vela-stop',
    script: 'vela-stop.js',
    description: '⛵ Checking active voyage...'
  },
  {
    matcher: 'SessionStart',
    hookId: 'vela-session-start',
    script: 'vela-session-start.js',
    description: '⛵ Scanning for interrupted voyages...'
  },
  {
    matcher: 'PreCompact',
    hookId: 'vela-compact',
    script: 'vela-compact.js',
    description: '✦ Preserving navigation state...'
  },
  {
    matcher: 'PostCompact',
    hookId: 'vela-compact',
    script: 'vela-compact.js',
    description: '✦ Restoring navigation state...'
  },
  {
    matcher: 'SubagentStart',
    hookId: 'vela-subagent-start',
    script: 'vela-subagent-start.js',
    description: '⛵ Briefing crew member...'
  },
  {
    matcher: 'TaskCompleted',
    hookId: 'vela-task-completed',
    script: 'vela-task-completed.js',
    description: '✦ Verifying voyage milestone...'
  },
  {
    matcher: 'SubagentStop',
    hookId: 'vela-subagent-stop',
    script: 'vela-subagent-stop.js',
    description: '⛵ Harvesting crew report...'
  },
  {
    matcher: 'PermissionRequest',
    hookId: 'vela-permission',
    script: 'vela-permission.js',
    if: 'Write(*)|Edit(*)|NotebookEdit(*)',
    description: '⛵ Checking crew clearance...'
  },
  {
    matcher: 'PostToolUseFailure',
    hookId: 'vela-failure',
    script: 'vela-failure.js',
    description: '🔭 Recording tool failure...'
  },
  {
    matcher: 'StopFailure',
    hookId: 'vela-stop-failure',
    script: 'vela-stop-failure.js',
    description: '🛟 Preserving voyage state...'
  },
  {
    matcher: 'TeammateIdle',
    hookId: 'vela-teammate-idle',
    script: 'vela-teammate-idle.js',
    description: '⛵ Checking idle crew...'
  },
  {
    matcher: 'PostToolUse',
    hookId: 'vela-review-prompt',
    hookType: 'prompt',
    toolMatcher: 'Edit|Write',
    prompt: 'Review this code change for bugs, security issues, and style problems. Context: $ARGUMENTS. Respond with JSON: {"ok": true} if acceptable, or {"ok": false, "reason": "description of issues found"} if there are problems. Focus on: 1) Logic errors 2) Security vulnerabilities 3) Missing error handling 4) Breaking changes.',
    description: '🔍 Reviewing code change...'
  },
  {
    matcher: 'PostToolUse',
    hookId: 'vela-test-async',
    hookType: 'command',
    toolMatcher: 'Edit|Write',
    script: 'vela-test-async.js',
    async: true,
    description: '🧪 Running related tests...'
  },
  {
    matcher: 'Notification',
    hookId: 'vela-notification',
    script: 'vela-notification.js',
    description: '🔔 Sending desktop notification...'
  }
];

const command = (process.argv[2] && !process.argv[2].startsWith('-')) ? process.argv[2] : 'install';

switch (command) {
  case 'install': install(); break;
  case 'verify': verify(); break;
  case 'uninstall': uninstall(); break;
  case 'status': status(); break;
  case 'upgrade': upgrade(); break;
  default:
    console.log(JSON.stringify({ ok: false, error: `Unknown command: ${command}` }));
    process.exit(1);
}

function install() {
  // ─── Phase 0: Validate & Repair ───
  const validation = validate();

  // Ensure .claude/ directory exists
  const claudeDir = path.join(PROJECT_ROOT, '.claude');
  if (!fs.existsSync(claudeDir)) {
    fs.mkdirSync(claudeDir, { recursive: true });
  }

  const settings = readSettings();

  if (!settings.hooks) {
    settings.hooks = {};
  }

  const installed = [];
  const errors = [];

  for (const hook of VELA_HOOKS) {
    const scriptPath = path.join(VELA_HOOKS_DIR, hook.script || '');

    // Verify script exists (skip for prompt-type hooks which have no script)
    if (hook.hookType !== 'prompt') {
      if (!fs.existsSync(scriptPath)) {
        errors.push(`Script not found: ${scriptPath}`);
        continue;
      }
    }

    // Initialize event array if needed
    if (!settings.hooks[hook.matcher]) {
      settings.hooks[hook.matcher] = [];
    }

    // Remove existing Vela hook entry with same ID (both legacy and new format)
    settings.hooks[hook.matcher] = settings.hooks[hook.matcher].filter(entry => {
      // Remove legacy flat format
      if (entry.command && !entry.hooks && entry.command.includes(hook.hookId)) return false;
      // Remove new nested format
      if (entry.hooks && Array.isArray(entry.hooks)) {
        return !entry.hooks.some(h =>
          (h.command && h.command.includes(hook.hookId)) ||
          (h.prompt && h.type === 'prompt' && entry._velaId === hook.hookId)
        );
      }
      return true;
    });

    // Build hookEntry based on hookType
    let hookEntry;
    if (hook.hookType === 'prompt') {
      hookEntry = {
        type: 'prompt',
        prompt: hook.prompt,
        timeout: 30,
        statusMessage: hook.description
      };
    } else {
      hookEntry = {
        type: 'command',
        command: `node "${scriptPath}"`,
        statusMessage: hook.description
      };
    }

    // Add async flag if present
    if (hook.async) {
      hookEntry.async = true;
    }

    // Add model field if present
    if (hook.model) {
      hookEntry.model = hook.model;
    }

    // Add if condition if present (tool events only)
    if (hook.if) {
      hookEntry.if = hook.if;
    }

    // Add the hook in correct Claude Code format:
    // { matcher: "ToolName", hooks: [{ type: "command"|"prompt", ... }] }
    const settingsEntry = {
      matcher: hook.toolMatcher || '',
      hooks: [hookEntry]
    };

    // Tag with vela ID for prompt hooks (no command string to match on)
    if (hook.hookType === 'prompt') {
      settingsEntry._velaId = hook.hookId;
    }

    settings.hooks[hook.matcher].push(settingsEntry);

    installed.push(hook.hookId);
  }

  // ─── Register permission rules ───
  if (!settings.permissions) {
    settings.permissions = {};
  }

  // Merge deny rules (deduplicate)
  const existingDeny = new Set(settings.permissions.deny || []);
  for (const rule of VELA_PERMISSIONS.deny) {
    existingDeny.add(rule);
  }
  settings.permissions.deny = [...existingDeny];

  // Merge allow rules (deduplicate)
  const existingAllow = new Set(settings.permissions.allow || []);
  for (const rule of VELA_PERMISSIONS.allow) {
    existingAllow.add(rule);
  }
  settings.permissions.allow = [...existingAllow];

  // ─── Set default agent to vela ───
  settings.agent = 'vela';

  // ─── Set statusLine ───
  const statusLinePath = path.join(PROJECT_ROOT, '.vela', 'statusline.sh');
  if (fs.existsSync(statusLinePath)) {
    settings.statusLine = {
      type: 'command',
      command: statusLinePath,
      padding: 2
    };
  }

  // ─── Spinner Verbs (항해 테마) ───
  settings.spinnerVerbs = {
    mode: 'replace',
    verbs: [
      '⛵ Navigating', '🧭 Charting', '✦ Stargazing',
      '🔭 Observing', '⚓ Anchoring', '🌟 Reading Stars',
      '🧭 Plotting Course', '⛵ Setting Sail', '✦ Crossing Meridian',
      '🔭 Scanning Horizon', '⛵ Catching Wind', '🌟 Trimming Sails'
    ]
  };

  // ─── Spinner Tips (Vela 철학) ───
  settings.spinnerTipsOverride = {
    excludeDefault: true,
    tips: [
      '⛵ 별을 따라 항해하라 — 모든 파이프라인은 목적지로 향한다',
      '🌟 품질은 지시가 아닌 구조로 강제된다',
      '🧭 연구 → 계획 → 실행 → 검증 — 항로를 건너뛰지 마라',
      '✦ Reviewer는 독립적으로 판단한다 — 편향 없는 별빛',
      '⛵ Vela(돛자리)는 하늘에서 가장 큰 별자리의 일부였다',
      '🔭 research.md → plan.md → approval.json — 항해의 기록',
      '🧭 /vela:start 로 새로운 항해를 시작하세요',
      '✦ 같은 세션에서 자기 작업을 검증하면 편향이 생긴다',
      '⛵ Gate Keeper는 수문장, Gate Guard는 가이드라인',
      '🌟 approval 없이는 다음 항구로 갈 수 없다',
      '🔭 Agent Teams — 독립된 선원들이 각자의 관점으로 항해한다',
      '⛵ 구조로 강제하라, 지시로 의존하지 마라'
    ]
  };

  // ─── Startup Announcements ───
  settings.companyAnnouncements = [
    '⛵ Vela Engine — 별자리가 항해를 안내합니다. /vela:start 로 파이프라인을 시작하세요.',
    '✦ Vela — 구조로 강제하고, 독립으로 검증하고, 기록으로 추적합니다.',
    '🧭 Vela Pipeline — 연구 → 계획 → 실행 → 검증. 항로를 따르세요.',
    '⛵ 모든 항해에는 별자리가 길을 안내합니다. Vela와 함께.'
  ];

  // ─── Attribution (커밋/PR에 Vela 참조) ───
  settings.attribution = {
    commit: '⛵ Managed by Vela Engine (https://github.com/EcoKG/vela)',
    pr: '⛵ This PR was managed by [Vela Engine](https://github.com/EcoKG/vela) — pipeline-driven development governance.'
  };

  // ─── Auto Mode (sandbox-safe bash auto-allow) ───
  settings.autoMode = {
    allow: [
      'Bash commands within .vela/ directory',
      'Bash commands for git status, log, diff, branch',
      'Read operations on any file'
    ],
    soft_deny: [
      'Bash commands that modify files outside .vela/',
      'Git push, reset, clean operations'
    ],
    environment: [
      'Project uses Vela pipeline governance',
      'All modifications require active pipeline'
    ]
  };

  writeSettings(settings);

  // Create state directory for session tracking (project-local)
  const stateDir = path.join(PROJECT_ROOT, '.vela', 'state');
  if (!fs.existsSync(stateDir)) {
    fs.mkdirSync(stateDir, { recursive: true });
  }

  // ─── Deploy vela agent ───
  const agentsDir = path.join(PROJECT_ROOT, '.claude', 'agents');
  if (!fs.existsSync(agentsDir)) {
    fs.mkdirSync(agentsDir, { recursive: true });
  }
  const pmSourcePath = path.join(PROJECT_ROOT, '.vela', 'agents', 'vela.md');
  const pmTargetPath = path.join(agentsDir, 'vela.md');
  if (fs.existsSync(pmSourcePath)) {
    fs.copyFileSync(pmSourcePath, pmTargetPath);
  }

  // ─── Create CLAUDE.md if not exists ───
  const claudeMdPath = path.join(PROJECT_ROOT, 'CLAUDE.md');
  if (!fs.existsSync(claudeMdPath)) {
    fs.writeFileSync(claudeMdPath, `# Development Workflow — Vela

This project uses Vela for development governance.

- To explore/read code: use normal tools freely (Explore mode).
- To modify code: ALWAYS start with \`node .vela/cli/vela-engine.js init "<task>" --scale <small|medium|large>\`
- Follow pipeline steps in order. Do NOT use TaskCreate/TaskUpdate during pipeline execution.
- Do NOT skip pipeline steps or create your own plans outside the pipeline.
- Each team step uses Teammate (소통 필요) or Subagent (독립 작업). Model: Haiku(탐색), Sonnet(코딩/리뷰), Opus(설계/분석).
`);
  }

  const permissionCount = VELA_PERMISSIONS.deny.length + VELA_PERMISSIONS.allow.length;

  // Human-readable output (JSON with --json flag)
  if (process.argv.includes('--json')) {
    console.log(JSON.stringify({
      ok: errors.length === 0, command: 'install', validation, installed,
      agent: 'vela', permissions: { deny: VELA_PERMISSIONS.deny.length, allow: VELA_PERMISSIONS.allow.length },
      errors, settings_path: SETTINGS_PATH
    }, null, 2));
  } else {
    console.log('');
    console.log('✦ Vela Engine — Installation Complete ✦');
    console.log('');
    console.log(`  ⛵ Hooks: ${installed.length} registered`);
    installed.forEach(h => console.log(`     ✓ ${h}`));
    console.log(`  🌟 Permissions: ${VELA_PERMISSIONS.deny.length} deny + ${VELA_PERMISSIONS.allow.length} allow`);
    console.log(`  🧭 Agent: vela`);
    console.log(`  🔭 StatusLine: active`);
    console.log(`  ✦ Spinner: ${12} nautical verbs`);
    console.log(`  ⛵ CLAUDE.md: ${fs.existsSync(claudeMdPath) ? 'exists' : 'created'}`);
    if (validation.fixed.length > 0) {
      console.log('');
      console.log('  🔧 Auto-repaired:');
      validation.fixed.forEach(f => console.log(`     ✓ ${f}`));
    }
    if (validation.warnings.length > 0) {
      console.log('');
      console.log('  ⚠ Warnings:');
      validation.warnings.forEach(w => console.log(`     ! ${w}`));
    }
    if (errors.length > 0) {
      console.log('');
      console.log('  ❌ Errors:');
      errors.forEach(e => console.log(`     ✗ ${e}`));
    }
    console.log('');
    console.log('✦─────────────────────✦');
    console.log('');
  }
}

function verify() {
  const settings = readSettings();
  const results = [];

  for (const hook of VELA_HOOKS) {
    const scriptPath = path.join(VELA_HOOKS_DIR, hook.script || '');

    // For prompt hooks, no script file to check
    const scriptExists = hook.hookType === 'prompt' ? true : fs.existsSync(scriptPath);

    const matcherHooks = settings.hooks?.[hook.matcher] || [];
    let registered;

    if (hook.hookType === 'prompt') {
      // Prompt hooks: check for type:'prompt' entry with matching _velaId or prompt content
      registered = matcherHooks.some(entry =>
        entry.hooks && Array.isArray(entry.hooks) &&
        entry.hooks.some(h => h.type === 'prompt' && h.prompt) &&
        (entry._velaId === hook.hookId || (entry.hooks.some(h => h.prompt === hook.prompt)))
      );
    } else {
      // Command hooks: check for command string containing hookId
      registered = matcherHooks.some(entry =>
        entry.hooks && Array.isArray(entry.hooks) &&
        entry.hooks.some(h => h.command && h.command.includes(hook.hookId))
      );
    }

    results.push({
      id: hook.hookId,
      matcher: hook.matcher,
      hookType: hook.hookType || 'command',
      script_exists: scriptExists,
      registered: registered,
      status: scriptExists && registered ? 'OK' : 'MISSING'
    });
  }

  const allOk = results.every(r => r.status === 'OK');

  console.log(JSON.stringify({
    ok: allOk,
    command: 'verify',
    hooks: results,
    message: allOk
      ? 'All Vela hooks verified successfully.'
      : 'Some hooks are missing or not registered.'
  }, null, 2));
}

function uninstall() {
  const settings = readSettings();
  let removedHooks = 0;
  let removedPerms = 0;

  // Remove hooks (both new and legacy format)
  if (settings.hooks) {
    for (const matcher of Object.keys(settings.hooks)) {
      const before = settings.hooks[matcher].length;
      settings.hooks[matcher] = settings.hooks[matcher].filter(entry => {
        // Remove legacy flat format: { command: "...vela...", description: "..." }
        if (entry.command && !entry.hooks && entry.command.includes(HOOK_PREFIX)) return false;
        // Remove new nested format: { matcher, hooks: [{ command: "...vela..." }] }
        if (entry.hooks && Array.isArray(entry.hooks)) {
          return !entry.hooks.some(h => h.command && h.command.includes(HOOK_PREFIX));
        }
        return true;
      });
      removedHooks += before - settings.hooks[matcher].length;

      if (settings.hooks[matcher].length === 0) {
        delete settings.hooks[matcher];
      }
    }

    if (Object.keys(settings.hooks).length === 0) {
      delete settings.hooks;
    }
  }

  // Remove Vela permission rules
  if (settings.permissions) {
    const velaRules = new Set([...VELA_PERMISSIONS.deny, ...VELA_PERMISSIONS.allow]);

    if (settings.permissions.deny) {
      const before = settings.permissions.deny.length;
      settings.permissions.deny = settings.permissions.deny.filter(r => !velaRules.has(r));
      removedPerms += before - settings.permissions.deny.length;
      if (settings.permissions.deny.length === 0) delete settings.permissions.deny;
    }

    if (settings.permissions.allow) {
      const before = settings.permissions.allow.length;
      settings.permissions.allow = settings.permissions.allow.filter(r => !velaRules.has(r));
      removedPerms += before - settings.permissions.allow.length;
      if (settings.permissions.allow.length === 0) delete settings.permissions.allow;
    }

    if (Object.keys(settings.permissions).length === 0) {
      delete settings.permissions;
    }
  }

  writeSettings(settings);

  console.log(JSON.stringify({
    ok: true,
    command: 'uninstall',
    removed_hooks: removedHooks,
    removed_permissions: removedPerms,
    message: `Removed ${removedHooks} hooks + ${removedPerms} permission rules.`
  }, null, 2));
}

function status() {
  const settings = readSettings();
  const registered = [];

  if (settings.hooks) {
    for (const [event, entries] of Object.entries(settings.hooks)) {
      for (const entry of entries) {
        if (entry.hooks && Array.isArray(entry.hooks)) {
          for (const hook of entry.hooks) {
            if (hook.command && hook.command.includes(HOOK_PREFIX)) {
              registered.push({
                event,
                matcher: entry.matcher || '',
                command: hook.command,
                description: hook.statusMessage || ''
              });
            }
          }
        }
      }
    }
  }

  // Check permissions
  const permissions = {
    deny: (settings.permissions?.deny || []).filter(r => VELA_PERMISSIONS.deny.includes(r)),
    allow: (settings.permissions?.allow || []).filter(r => VELA_PERMISSIONS.allow.includes(r))
  };

  console.log(JSON.stringify({
    ok: true,
    command: 'status',
    vela_hooks: registered,
    hook_count: registered.length,
    vela_permissions: permissions,
    permission_count: permissions.deny.length + permissions.allow.length,
    settings_path: SETTINGS_PATH
  }, null, 2));
}

// ─── Upgrade ───

function upgrade() {
  const velaDir = path.join(PROJECT_ROOT, '.vela');
  if (!fs.existsSync(velaDir)) {
    console.log(JSON.stringify({ ok: false, error: 'Vela not installed. Run install first.' }));
    process.exit(1);
  }

  const skillBase = path.resolve(__dirname, '..');
  const results = { updated: [], added: [], skipped: [], errors: [] };

  // Same file list as validate()
  const upgradeFiles = [
    { src: 'scripts/hooks/vela-gate-keeper.js', dst: 'hooks/vela-gate-keeper.js' },
    { src: 'scripts/hooks/vela-gate-guard.js', dst: 'hooks/vela-gate-guard.js' },
    { src: 'scripts/hooks/vela-orchestrator.js', dst: 'hooks/vela-orchestrator.js' },
    { src: 'scripts/hooks/vela-tracker.js', dst: 'hooks/vela-tracker.js' },
    { src: 'scripts/hooks/vela-stop.js', dst: 'hooks/vela-stop.js' },
    { src: 'scripts/hooks/vela-session-start.js', dst: 'hooks/vela-session-start.js' },
    { src: 'scripts/hooks/vela-compact.js', dst: 'hooks/vela-compact.js' },
    { src: 'scripts/hooks/vela-subagent-start.js', dst: 'hooks/vela-subagent-start.js' },
    { src: 'scripts/hooks/vela-task-completed.js', dst: 'hooks/vela-task-completed.js' },
    { src: 'scripts/hooks/vela-subagent-stop.js', dst: 'hooks/vela-subagent-stop.js' },
    { src: 'scripts/hooks/vela-permission.js', dst: 'hooks/vela-permission.js' },
    { src: 'scripts/hooks/vela-failure.js', dst: 'hooks/vela-failure.js' },
    { src: 'scripts/hooks/vela-stop-failure.js', dst: 'hooks/vela-stop-failure.js' },
    { src: 'scripts/hooks/vela-teammate-idle.js', dst: 'hooks/vela-teammate-idle.js' },
    { src: 'scripts/hooks/vela-test-async.js', dst: 'hooks/vela-test-async.js' },
    { src: 'scripts/hooks/vela-notification.js', dst: 'hooks/vela-notification.js' },
    { src: 'scripts/hooks/shared/constants.js', dst: 'hooks/shared/constants.js' },
    { src: 'scripts/hooks/shared/pipeline.js', dst: 'hooks/shared/pipeline.js' },
    { src: 'scripts/cli/vela-engine.js', dst: 'cli/vela-engine.js' },
    { src: 'scripts/cli/vela-read.js', dst: 'cli/vela-read.js' },
    { src: 'scripts/cli/vela-write.js', dst: 'cli/vela-write.js' },
    { src: 'scripts/cache/treenode.js', dst: 'cache/treenode.js' },
    { src: 'scripts/statusline.sh', dst: 'statusline.sh' },
    { src: 'scripts/agents/vela.md', dst: 'agents/vela.md' },
    { src: 'scripts/agents/researcher.md', dst: 'agents/researcher.md' },
    { src: 'scripts/agents/planner.md', dst: 'agents/planner.md' },
    { src: 'scripts/agents/executor.md', dst: 'agents/executor.md' },
    { src: 'scripts/agents/reviewer.md', dst: 'agents/reviewer.md' },
    { src: 'scripts/agents/leader.md', dst: 'agents/leader.md' },
    { src: 'scripts/agents/conflict-manager.md', dst: 'agents/conflict-manager.md' },
    { src: 'templates/pipeline.json', dst: 'templates/pipeline.json' },
    { src: 'references/interactive-ui.md', dst: 'references/interactive-ui.md' },
    { src: 'references/gates-and-guards.md', dst: 'references/gates-and-guards.md' },
    { src: 'references/cli-reference.md', dst: 'references/cli-reference.md' },
    { src: 'references/messages-en.md', dst: 'references/messages-en.md' },
    // Agent tree
    { src: 'scripts/agents/pm/index.md', dst: 'agents/pm/index.md' },
    { src: 'scripts/agents/pm/prompt-optimizer.md', dst: 'agents/pm/prompt-optimizer.md' },
    { src: 'scripts/agents/pm/pipeline-flow.md', dst: 'agents/pm/pipeline-flow.md' },
    { src: 'scripts/agents/pm/team-rules.md', dst: 'agents/pm/team-rules.md' },
    { src: 'scripts/agents/pm/model-strategy.md', dst: 'agents/pm/model-strategy.md' },
    { src: 'scripts/agents/pm/block-recovery.md', dst: 'agents/pm/block-recovery.md' },
    { src: 'scripts/agents/researcher/index.md', dst: 'agents/researcher/index.md' },
    { src: 'scripts/agents/researcher/hypothesis.md', dst: 'agents/researcher/hypothesis.md' },
    { src: 'scripts/agents/researcher/security.md', dst: 'agents/researcher/security.md' },
    { src: 'scripts/agents/researcher/architecture.md', dst: 'agents/researcher/architecture.md' },
    { src: 'scripts/agents/researcher/quality.md', dst: 'agents/researcher/quality.md' },
    { src: 'scripts/agents/executor/index.md', dst: 'agents/executor/index.md' },
    { src: 'scripts/agents/executor/tdd.md', dst: 'agents/executor/tdd.md' },
    { src: 'scripts/agents/executor/file-ownership.md', dst: 'agents/executor/file-ownership.md' },
    { src: 'scripts/agents/executor/worktree.md', dst: 'agents/executor/worktree.md' },
    { src: 'scripts/agents/planner/index.md', dst: 'agents/planner/index.md' },
    { src: 'scripts/agents/planner/spec-format.md', dst: 'agents/planner/spec-format.md' },
    { src: 'scripts/agents/planner/crosslayer.md', dst: 'agents/planner/crosslayer.md' },
    { src: 'scripts/agents/reviewer/index.md', dst: 'agents/reviewer/index.md' },
    { src: 'scripts/agents/reviewer/scoring.md', dst: 'agents/reviewer/scoring.md' },
    { src: 'scripts/agents/conflict-manager/index.md', dst: 'agents/conflict-manager/index.md' },
    { src: 'scripts/agents/conflict-manager/merge-procedure.md', dst: 'agents/conflict-manager/merge-procedure.md' },
    { src: 'scripts/agents/conflict-manager/interface-watch.md', dst: 'agents/conflict-manager/interface-watch.md' },
    { src: 'scripts/guidelines/index.md', dst: 'guidelines/index.md' },
    { src: 'scripts/guidelines/coding-standards.md', dst: 'guidelines/coding-standards.md' },
    { src: 'scripts/guidelines/error-handling.md', dst: 'guidelines/error-handling.md' },
    { src: 'scripts/guidelines/testing-strategy.md', dst: 'guidelines/testing-strategy.md' },
    // SDK modules (optional — require @anthropic-ai/claude-agent-sdk)
    { src: 'scripts/hooks/shared/sdk-runner.js', dst: 'hooks/shared/sdk-runner.js' },
    { src: 'scripts/hooks/shared/sdk-reviewer.js', dst: 'hooks/shared/sdk-reviewer.js' },
    { src: 'scripts/hooks/shared/sdk-plan-checker.js', dst: 'hooks/shared/sdk-plan-checker.js' },
    { src: 'scripts/hooks/shared/sdk-researcher.js', dst: 'hooks/shared/sdk-researcher.js' },
    { src: 'scripts/hooks/shared/sdk-executor.js', dst: 'hooks/shared/sdk-executor.js' },
  ];

  // Do NOT overwrite config.json (user may have customized it)
  // Do NOT overwrite install.js itself

  for (const f of upgradeFiles) {
    const srcPath = path.join(skillBase, f.src);
    const dstPath = path.join(velaDir, f.dst);

    if (!fs.existsSync(srcPath)) {
      results.skipped.push(f.dst);
      continue;
    }

    try {
      const dstDir = path.dirname(dstPath);
      if (!fs.existsSync(dstDir)) fs.mkdirSync(dstDir, { recursive: true });

      const isNew = !fs.existsSync(dstPath);
      fs.copyFileSync(srcPath, dstPath);

      if (isNew) {
        results.added.push(f.dst);
      } else {
        results.updated.push(f.dst);
      }
    } catch (e) {
      results.errors.push(`${f.dst}: ${e.message}`);
    }
  }

  // Also update the PM agent in .claude/agents/
  const pmSrc = path.join(velaDir, 'agents', 'vela.md');
  const pmDst = path.join(PROJECT_ROOT, '.claude', 'agents', 'vela.md');
  if (fs.existsSync(pmSrc) && fs.existsSync(path.dirname(pmDst))) {
    try {
      fs.copyFileSync(pmSrc, pmDst);
      results.updated.push('.claude/agents/vela.md');
    } catch (e) {
      results.errors.push(`.claude/agents/vela.md: ${e.message}`);
    }
  }

  console.log(JSON.stringify({
    ok: results.errors.length === 0,
    command: 'upgrade',
    updated: results.updated.length,
    added: results.added.length,
    skipped: results.skipped.length,
    errors: results.errors,
    details: results
  }, null, 2));
}

// ─── Validate & Repair ───

function validate() {
  const results = { fixed: [], warnings: [], ok: [] };
  const velaDir = path.join(PROJECT_ROOT, '.vela');

  // 1. Required directories
  const requiredDirs = [
    'hooks', 'hooks/shared', 'cli', 'cache', 'templates',
    'state', 'artifacts', 'agents', 'references', 'guidelines',
    'agents/pm', 'agents/researcher', 'agents/executor',
    'agents/planner', 'agents/reviewer', 'agents/conflict-manager'
  ];
  for (const dir of requiredDirs) {
    const dirPath = path.join(velaDir, dir);
    if (!fs.existsSync(dirPath)) {
      fs.mkdirSync(dirPath, { recursive: true });
      results.fixed.push(`Created missing directory: .vela/${dir}`);
    }
  }

  // 2. Required files — check and copy from skill if missing
  const skillBase = path.resolve(__dirname, '..');
  const requiredFiles = [
    { src: 'scripts/hooks/vela-gate-keeper.js', dst: 'hooks/vela-gate-keeper.js' },
    { src: 'scripts/hooks/vela-gate-guard.js', dst: 'hooks/vela-gate-guard.js' },
    { src: 'scripts/hooks/vela-orchestrator.js', dst: 'hooks/vela-orchestrator.js' },
    { src: 'scripts/hooks/vela-tracker.js', dst: 'hooks/vela-tracker.js' },
    { src: 'scripts/hooks/vela-stop.js', dst: 'hooks/vela-stop.js' },
    { src: 'scripts/hooks/vela-session-start.js', dst: 'hooks/vela-session-start.js' },
    { src: 'scripts/hooks/vela-compact.js', dst: 'hooks/vela-compact.js' },
    { src: 'scripts/hooks/vela-subagent-start.js', dst: 'hooks/vela-subagent-start.js' },
    { src: 'scripts/hooks/vela-task-completed.js', dst: 'hooks/vela-task-completed.js' },
    { src: 'scripts/hooks/vela-subagent-stop.js', dst: 'hooks/vela-subagent-stop.js' },
    { src: 'scripts/hooks/vela-permission.js', dst: 'hooks/vela-permission.js' },
    { src: 'scripts/hooks/vela-failure.js', dst: 'hooks/vela-failure.js' },
    { src: 'scripts/hooks/vela-stop-failure.js', dst: 'hooks/vela-stop-failure.js' },
    { src: 'scripts/hooks/vela-teammate-idle.js', dst: 'hooks/vela-teammate-idle.js' },
    { src: 'scripts/hooks/vela-test-async.js', dst: 'hooks/vela-test-async.js' },
    { src: 'scripts/hooks/vela-notification.js', dst: 'hooks/vela-notification.js' },
    { src: 'scripts/hooks/shared/constants.js', dst: 'hooks/shared/constants.js' },
    { src: 'scripts/hooks/shared/pipeline.js', dst: 'hooks/shared/pipeline.js' },
    { src: 'scripts/cli/vela-engine.js', dst: 'cli/vela-engine.js' },
    { src: 'scripts/cli/vela-read.js', dst: 'cli/vela-read.js' },
    { src: 'scripts/cli/vela-write.js', dst: 'cli/vela-write.js' },
    { src: 'scripts/cache/treenode.js', dst: 'cache/treenode.js' },
    { src: 'scripts/statusline.sh', dst: 'statusline.sh' },
    { src: 'scripts/agents/vela.md', dst: 'agents/vela.md' },
    { src: 'scripts/agents/researcher.md', dst: 'agents/researcher.md' },
    { src: 'scripts/agents/planner.md', dst: 'agents/planner.md' },
    { src: 'scripts/agents/executor.md', dst: 'agents/executor.md' },
    { src: 'scripts/agents/reviewer.md', dst: 'agents/reviewer.md' },
    { src: 'scripts/agents/leader.md', dst: 'agents/leader.md' },
    { src: 'scripts/agents/conflict-manager.md', dst: 'agents/conflict-manager.md' },
    { src: 'templates/pipeline.json', dst: 'templates/pipeline.json' },
    { src: 'templates/config.json', dst: 'templates/config.json' },
    { src: 'references/interactive-ui.md', dst: 'references/interactive-ui.md' },
    { src: 'references/gates-and-guards.md', dst: 'references/gates-and-guards.md' },
    { src: 'references/cli-reference.md', dst: 'references/cli-reference.md' },
    { src: 'references/messages-en.md', dst: 'references/messages-en.md' },
    // Agent tree structure
    { src: 'scripts/agents/pm/index.md', dst: 'agents/pm/index.md' },
    { src: 'scripts/agents/pm/prompt-optimizer.md', dst: 'agents/pm/prompt-optimizer.md' },
    { src: 'scripts/agents/pm/pipeline-flow.md', dst: 'agents/pm/pipeline-flow.md' },
    { src: 'scripts/agents/pm/team-rules.md', dst: 'agents/pm/team-rules.md' },
    { src: 'scripts/agents/pm/model-strategy.md', dst: 'agents/pm/model-strategy.md' },
    { src: 'scripts/agents/pm/block-recovery.md', dst: 'agents/pm/block-recovery.md' },
    { src: 'scripts/agents/researcher/index.md', dst: 'agents/researcher/index.md' },
    { src: 'scripts/agents/researcher/hypothesis.md', dst: 'agents/researcher/hypothesis.md' },
    { src: 'scripts/agents/researcher/security.md', dst: 'agents/researcher/security.md' },
    { src: 'scripts/agents/researcher/architecture.md', dst: 'agents/researcher/architecture.md' },
    { src: 'scripts/agents/researcher/quality.md', dst: 'agents/researcher/quality.md' },
    { src: 'scripts/agents/executor/index.md', dst: 'agents/executor/index.md' },
    { src: 'scripts/agents/executor/tdd.md', dst: 'agents/executor/tdd.md' },
    { src: 'scripts/agents/executor/file-ownership.md', dst: 'agents/executor/file-ownership.md' },
    { src: 'scripts/agents/executor/worktree.md', dst: 'agents/executor/worktree.md' },
    { src: 'scripts/agents/planner/index.md', dst: 'agents/planner/index.md' },
    { src: 'scripts/agents/planner/spec-format.md', dst: 'agents/planner/spec-format.md' },
    { src: 'scripts/agents/planner/crosslayer.md', dst: 'agents/planner/crosslayer.md' },
    { src: 'scripts/agents/reviewer/index.md', dst: 'agents/reviewer/index.md' },
    { src: 'scripts/agents/reviewer/scoring.md', dst: 'agents/reviewer/scoring.md' },
    { src: 'scripts/agents/conflict-manager/index.md', dst: 'agents/conflict-manager/index.md' },
    { src: 'scripts/agents/conflict-manager/merge-procedure.md', dst: 'agents/conflict-manager/merge-procedure.md' },
    { src: 'scripts/agents/conflict-manager/interface-watch.md', dst: 'agents/conflict-manager/interface-watch.md' },
    // Guidelines
    { src: 'scripts/guidelines/index.md', dst: 'guidelines/index.md' },
    { src: 'scripts/guidelines/coding-standards.md', dst: 'guidelines/coding-standards.md' },
    { src: 'scripts/guidelines/error-handling.md', dst: 'guidelines/error-handling.md' },
    { src: 'scripts/guidelines/testing-strategy.md', dst: 'guidelines/testing-strategy.md' },
    // SDK modules (optional — require @anthropic-ai/claude-agent-sdk)
    { src: 'scripts/hooks/shared/sdk-runner.js', dst: 'hooks/shared/sdk-runner.js' },
    { src: 'scripts/hooks/shared/sdk-reviewer.js', dst: 'hooks/shared/sdk-reviewer.js' },
    { src: 'scripts/hooks/shared/sdk-plan-checker.js', dst: 'hooks/shared/sdk-plan-checker.js' },
    { src: 'scripts/hooks/shared/sdk-researcher.js', dst: 'hooks/shared/sdk-researcher.js' },
    { src: 'scripts/hooks/shared/sdk-executor.js', dst: 'hooks/shared/sdk-executor.js' }
  ];

  for (const f of requiredFiles) {
    const dstPath = path.join(velaDir, f.dst);
    if (!fs.existsSync(dstPath)) {
      // Try to copy from skill directory
      const srcPath = path.join(skillBase, f.src);
      if (fs.existsSync(srcPath)) {
        const dstDir = path.dirname(dstPath);
        if (!fs.existsSync(dstDir)) fs.mkdirSync(dstDir, { recursive: true });
        fs.copyFileSync(srcPath, dstPath);
        results.fixed.push(`Restored missing file: .vela/${f.dst}`);
      } else {
        results.warnings.push(`Missing file: .vela/${f.dst} (source not found)`);
      }
    } else {
      results.ok.push(f.dst);
    }
  }

  // 3. config.json validity
  const configPath = path.join(velaDir, 'config.json');
  if (fs.existsSync(configPath)) {
    try {
      JSON.parse(fs.readFileSync(configPath, 'utf-8'));
    } catch (e) {
      // Broken config — restore from template
      const templateConfig = path.join(velaDir, 'templates', 'config.json');
      if (fs.existsSync(templateConfig)) {
        fs.copyFileSync(templateConfig, configPath);
        results.fixed.push('Repaired broken config.json from template');
      }
    }
  }

  // 4. Clean up old/legacy files
  const legacyFiles = [
    path.join(velaDir, 'hooks', 'vela-pm.md'),  // old agent name
  ];
  for (const lf of legacyFiles) {
    if (fs.existsSync(lf)) {
      fs.unlinkSync(lf);
      results.fixed.push(`Removed legacy file: ${path.basename(lf)}`);
    }
  }

  // 5. Fix settings.local.json — remove old format hooks
  if (fs.existsSync(SETTINGS_PATH)) {
    try {
      const settings = JSON.parse(fs.readFileSync(SETTINGS_PATH, 'utf-8'));
      let fixed = false;

      if (settings.hooks) {
        for (const event of Object.keys(settings.hooks)) {
          const before = settings.hooks[event].length;
          // Remove flat format hooks (legacy)
          settings.hooks[event] = settings.hooks[event].filter(entry => {
            if (entry.command && !entry.hooks) return false; // legacy flat format
            return true;
          });
          if (settings.hooks[event].length !== before) fixed = true;
        }
      }

      // Remove old agent name
      if (settings.agent === 'vela-pm') {
        settings.agent = 'vela';
        fixed = true;
      }

      if (fixed) {
        fs.writeFileSync(SETTINGS_PATH, JSON.stringify(settings, null, 2));
        results.fixed.push('Cleaned legacy hooks/settings from settings.local.json');
      }
    } catch (e) {
      // Broken settings — will be overwritten by install
      results.fixed.push('settings.local.json was broken, will be recreated');
    }
  }

  // 6. Statusline.sh line endings (CRLF → LF)
  const statuslinePath = path.join(velaDir, 'statusline.sh');
  if (fs.existsSync(statuslinePath)) {
    const content = fs.readFileSync(statuslinePath, 'utf-8');
    if (content.includes('\r\n')) {
      fs.writeFileSync(statuslinePath, content.replace(/\r\n/g, '\n'));
      results.fixed.push('Fixed CRLF line endings in statusline.sh');
    }
  }

  // 7. .gitignore — ensure all Vela files are hidden from git
  const { execSync } = require('child_process');
  const gitignorePath = path.join(PROJECT_ROOT, '.gitignore');
  const velaGitEntries = ['.vela/', '.claude/', 'CLAUDE.md'];

  // Step 1: Remove already-tracked Vela files BEFORE updating .gitignore
  try {
    const tracked = execSync('git ls-files .vela/ .claude/ CLAUDE.md', {
      cwd: PROJECT_ROOT, stdio: ['pipe', 'pipe', 'pipe'], timeout: 5000
    }).toString().trim();
    if (tracked) {
      execSync('git rm -r --cached --ignore-unmatch .vela/ .claude/ CLAUDE.md', {
        cwd: PROJECT_ROOT, stdio: 'pipe', timeout: 10000
      });
      execSync('git commit -m "chore: untrack Vela files from git" --no-verify', {
        cwd: PROJECT_ROOT, stdio: 'pipe', timeout: 10000
      });
      results.fixed.push('Removed Vela files from git tracking (files kept on disk)');
    }
  } catch (e) {
    // Not a git repo or git not available
  }

  // Step 2: Update .gitignore (after deletions are committed)
  let gitignoreContent = '';
  if (fs.existsSync(gitignorePath)) {
    gitignoreContent = fs.readFileSync(gitignorePath, 'utf-8');
  }
  const missingGit = velaGitEntries.filter(e => !gitignoreContent.includes(e));
  if (missingGit.length > 0) {
    const block = gitignoreContent.includes('# Vela Engine')
      ? missingGit.join('\n') + '\n'
      : '\n# Vela Engine (auto-managed)\n' + velaGitEntries.join('\n') + '\n';
    fs.appendFileSync(gitignorePath, block);
    results.fixed.push(`Added ${missingGit.length} entries to .gitignore: ${missingGit.join(', ')}`);
  }

  // 8. System dependencies — install if missing

  // jq (required for statusline.sh)
  try {
    execSync('which jq', { stdio: 'pipe' });
    results.ok.push('jq');
  } catch (e) {
    // Try to install jq
    const platform = process.platform;
    let installed = false;
    const cmds = [
      'sudo apt-get install -y jq 2>/dev/null',
      'sudo yum install -y jq 2>/dev/null',
      'brew install jq 2>/dev/null',
      'apk add jq 2>/dev/null'
    ];
    for (const cmd of cmds) {
      try {
        execSync(cmd, { stdio: 'pipe', timeout: 30000 });
        installed = true;
        results.fixed.push('Installed missing dependency: jq');
        break;
      } catch (e2) {}
    }
    if (!installed) {
      results.warnings.push('jq not found and auto-install failed. Install manually: sudo apt install jq');
    }
  }

  // SQLite backend for TreeNode cache (optional — multiple fallbacks available)
  let sqliteBackend = 'none';
  try { require('better-sqlite3'); sqliteBackend = 'better-sqlite3'; }
  catch (e) {
    try { require('sql.js'); sqliteBackend = 'sql.js'; }
    catch (e2) {
      try { execSync('which sqlite3', { stdio: 'pipe' }); sqliteBackend = 'sqlite3-cli'; }
      catch (e3) { /* will use JSON fallback */ }
    }
  }
  if (sqliteBackend !== 'none') {
    results.ok.push(`TreeNode cache: ${sqliteBackend}`);
  } else {
    results.warnings.push('No SQLite backend found — TreeNode cache will use JSON fallback. Run: npm install better-sqlite3 (or sql.js for WSL1/proxy)');
  }

  return results;
}

// ─── Settings I/O ───

function readSettings() {
  if (!fs.existsSync(SETTINGS_PATH)) {
    // Create settings directory if needed
    const dir = path.dirname(SETTINGS_PATH);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
    return {};
  }

  try {
    return JSON.parse(fs.readFileSync(SETTINGS_PATH, 'utf-8'));
  } catch (e) {
    return {};
  }
}

function writeSettings(settings) {
  const dir = path.dirname(SETTINGS_PATH);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  // Direct write (atomic rename fails on some WSL+Windows filesystems)
  fs.writeFileSync(SETTINGS_PATH, JSON.stringify(settings, null, 2));
}
