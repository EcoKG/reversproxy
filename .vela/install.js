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
  }
];

const command = process.argv[2] || 'install';

switch (command) {
  case 'install': install(); break;
  case 'verify': verify(); break;
  case 'uninstall': uninstall(); break;
  case 'status': status(); break;
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
    const scriptPath = path.join(VELA_HOOKS_DIR, hook.script);

    // Verify script exists
    if (!fs.existsSync(scriptPath)) {
      errors.push(`Script not found: ${scriptPath}`);
      continue;
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
        return !entry.hooks.some(h => h.command && h.command.includes(hook.hookId));
      }
      return true;
    });

    // Add the hook in correct Claude Code format:
    // { matcher: "ToolName", hooks: [{ type: "command", command: "..." }] }
    settings.hooks[hook.matcher].push({
      matcher: hook.toolMatcher || '',
      hooks: [{
        type: 'command',
        command: `node "${scriptPath}"`,
        statusMessage: hook.description
      }]
    });

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
- Each team step (research, plan, execute) uses Agent Teams with independent Worker → Reviewer → Leader agents.
`);
  }

  const permissionCount = VELA_PERMISSIONS.deny.length + VELA_PERMISSIONS.allow.length;

  console.log(JSON.stringify({
    ok: errors.length === 0,
    command: 'install',
    validation: validation,
    installed: installed,
    agent: 'vela',
    claude_md: !fs.existsSync(claudeMdPath) ? 'created' : 'exists',
    permissions: {
      deny_rules: VELA_PERMISSIONS.deny.length,
      allow_rules: VELA_PERMISSIONS.allow.length
    },
    errors: errors,
    settings_path: SETTINGS_PATH,
    message: errors.length === 0
      ? `Successfully installed ${installed.length} Vela hooks + ${permissionCount} permission rules + vela agent.`
      : `Installed ${installed.length} hooks with ${errors.length} errors.`
  }, null, 2));
}

function verify() {
  const settings = readSettings();
  const results = [];

  for (const hook of VELA_HOOKS) {
    const scriptPath = path.join(VELA_HOOKS_DIR, hook.script);
    const scriptExists = fs.existsSync(scriptPath);

    const matcherHooks = settings.hooks?.[hook.matcher] || [];
    const registered = matcherHooks.some(entry =>
      entry.hooks && Array.isArray(entry.hooks) &&
      entry.hooks.some(h => h.command && h.command.includes(hook.hookId))
    );

    results.push({
      id: hook.hookId,
      matcher: hook.matcher,
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

// ─── Validate & Repair ───

function validate() {
  const results = { fixed: [], warnings: [], ok: [] };
  const velaDir = path.join(PROJECT_ROOT, '.vela');

  // 1. Required directories
  const requiredDirs = [
    'hooks', 'hooks/shared', 'cli', 'cache', 'templates',
    'state', 'artifacts', 'agents'
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
    { src: 'templates/pipeline.json', dst: 'templates/pipeline.json' },
    { src: 'templates/config.json', dst: 'templates/config.json' }
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

  // 7. .gitignore — ensure Vela entries
  const gitignorePath = path.join(PROJECT_ROOT, '.gitignore');
  if (fs.existsSync(gitignorePath)) {
    const content = fs.readFileSync(gitignorePath, 'utf-8');
    const velaEntries = ['.vela/cache/', '.vela/state/', '.vela/artifacts/',
      '.vela/tracker-signals.json', '.vela/write-log.jsonl', '*.vela-tmp'];
    const missing = velaEntries.filter(e => !content.includes(e));
    if (missing.length > 0 && !content.includes('# Vela Engine')) {
      fs.appendFileSync(gitignorePath, '\n# Vela Engine (auto-managed)\n' + missing.join('\n') + '\n');
      results.fixed.push(`Added ${missing.length} entries to .gitignore`);
    }
  }

  // 8. System dependencies — install if missing
  const { execSync } = require('child_process');

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

  // sqlite3 (optional, for TreeNode cache)
  try {
    execSync('which sqlite3', { stdio: 'pipe' });
    results.ok.push('sqlite3');
  } catch (e) {
    results.warnings.push('sqlite3 not found (optional, for TreeNode cache)');
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
