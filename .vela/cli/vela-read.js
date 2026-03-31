#!/usr/bin/env node
/**
 * Vela Read CLI — Sandbox-aware file reading
 *
 * Reads files within the Vela sandbox, automatically updating the
 * TreeNode cache for path memory across sessions.
 *
 * Usage:
 *   vela-read <file-path> [--lines N] [--offset N]
 *   vela-read --glob <pattern> [--path DIR]
 *   vela-read --grep <pattern> [--path DIR] [--ext EXTS]
 *   vela-read --tree [--depth N]
 *   vela-read --cached               — Show cached paths from TreeNode
 *
 * Output: JSON with file content or search results
 */

const fs = require('fs');
const path = require('path');

const args = process.argv.slice(2);
const CWD = process.cwd();
const VELA_DIR = path.join(CWD, '.vela');
const CACHE_DIR = path.join(VELA_DIR, 'cache');

const SKIP_DIRS = new Set([
  'node_modules', '.git', 'dist', 'build', 'out', '.next', '.nuxt',
  'vendor', '__pycache__', '.venv', 'venv', '.cache', 'coverage',
  '.vela'
]);

// ─── Command Router ───
if (hasFlag('--glob')) {
  cmdGlob();
} else if (hasFlag('--grep')) {
  cmdGrep();
} else if (hasFlag('--tree')) {
  cmdTree();
} else if (hasFlag('--cached')) {
  cmdCached();
} else {
  cmdRead();
}

// ─── Read a single file ───
function cmdRead() {
  const filePath = args[0];
  if (!filePath) {
    return output({ ok: false, error: 'File path required. Usage: vela-read <file-path>' });
  }

  const absPath = path.isAbsolute(filePath) ? filePath : path.resolve(CWD, filePath);

  if (!fs.existsSync(absPath)) {
    return output({ ok: false, error: `File not found: ${absPath}` });
  }

  const stat = fs.statSync(absPath);
  if (stat.isDirectory()) {
    return output({ ok: false, error: `Path is a directory. Use --tree instead: ${absPath}` });
  }

  const lines = parseInt(getFlag('--lines') || '0', 10);
  const offset = parseInt(getFlag('--offset') || '0', 10);

  let content;
  try {
    content = fs.readFileSync(absPath, 'utf-8');
  } catch (e) {
    return output({ ok: false, error: `Cannot read file: ${e.message}` });
  }

  // Apply line limits
  if (lines > 0 || offset > 0) {
    const allLines = content.split('\n');
    const start = Math.max(0, offset);
    const end = lines > 0 ? start + lines : allLines.length;
    content = allLines.slice(start, end).join('\n');
  }

  // Record in TreeNode cache
  recordPath(absPath);

  output({
    ok: true,
    command: 'read',
    path: absPath,
    size: stat.size,
    lines: content.split('\n').length,
    content: content
  });
}

// ─── Glob search ───
function cmdGlob() {
  const pattern = getFlag('--glob');
  const searchPath = getFlag('--path') || CWD;

  if (!pattern) {
    return output({ ok: false, error: 'Pattern required. Usage: vela-read --glob "**/*.js"' });
  }

  // Simple glob implementation using recursive directory traversal
  const matches = [];
  const regex = globToRegex(pattern);

  walkDir(searchPath, (filePath) => {
    const relative = path.relative(searchPath, filePath);
    if (regex.test(relative)) {
      matches.push({
        path: filePath,
        relative: relative,
        size: fs.statSync(filePath).size
      });
      recordPath(filePath);
    }
  });

  output({
    ok: true,
    command: 'glob',
    pattern: pattern,
    base: searchPath,
    count: matches.length,
    matches: matches.slice(0, 200) // Cap results
  });
}

// ─── Grep search ───
function cmdGrep() {
  const pattern = getFlag('--grep');
  const searchPath = getFlag('--path') || CWD;
  const extFilter = getFlag('--ext');

  if (!pattern) {
    return output({ ok: false, error: 'Pattern required. Usage: vela-read --grep "searchTerm"' });
  }

  const regex = new RegExp(pattern, 'gi');
  const results = [];
  const extensions = extFilter ? extFilter.split(',').map(e => e.startsWith('.') ? e : `.${e}`) : null;

  walkDir(searchPath, (filePath) => {
    if (extensions) {
      const ext = path.extname(filePath).toLowerCase();
      if (!extensions.includes(ext)) return;
    }

    try {
      const content = fs.readFileSync(filePath, 'utf-8');
      const lines = content.split('\n');
      const matches = [];

      lines.forEach((line, idx) => {
        if (regex.test(line)) {
          matches.push({ line: idx + 1, text: line.trim().substring(0, 200) });
          regex.lastIndex = 0;
        }
      });

      if (matches.length > 0) {
        results.push({
          path: filePath,
          relative: path.relative(searchPath, filePath),
          matches: matches.slice(0, 10) // Cap per-file
        });
        recordPath(filePath);
      }
    } catch (e) {
      // Skip binary/unreadable files
    }
  });

  output({
    ok: true,
    command: 'grep',
    pattern: pattern,
    base: searchPath,
    file_count: results.length,
    results: results.slice(0, 50)
  });
}

// ─── Directory tree ───
function cmdTree() {
  const depth = parseInt(getFlag('--depth') || '3', 10);
  // Find basePath: skip flags, flag values, and the --tree flag itself
  const flagsWithValues = new Set(['--depth', '--lines', '--offset', '--path', '--ext', '--glob', '--grep']);
  const skipNext = new Set();
  const basePath = args.find((a, i) => {
    if (skipNext.has(i)) return false;
    if (a.startsWith('-')) {
      if (flagsWithValues.has(a)) skipNext.add(i + 1);
      return false;
    }
    return true;
  }) || CWD;
  const tree = buildTree(basePath, depth, 0);

  output({
    ok: true,
    command: 'tree',
    base: basePath,
    depth: depth,
    tree: tree
  });
}

// ─── Show cached paths ───
function cmdCached() {
  const pendingPath = path.join(CACHE_DIR, 'pending-paths.jsonl');
  const paths = [];

  if (fs.existsSync(pendingPath)) {
    try {
      const lines = fs.readFileSync(pendingPath, 'utf-8').trim().split('\n');
      const seen = new Set();
      for (const line of lines) {
        try {
          const entry = JSON.parse(line);
          if (!seen.has(entry.path)) {
            seen.add(entry.path);
            paths.push(entry);
          }
        } catch (e) {}
      }
    } catch (e) {}
  }

  output({
    ok: true,
    command: 'cached',
    count: paths.length,
    paths: paths
  });
}

// ─── Helpers ───

function walkDir(dir, callback) {
  try {
    const entries = fs.readdirSync(dir, { withFileTypes: true });
    for (const entry of entries) {
      if (SKIP_DIRS.has(entry.name)) continue;
      if (entry.name.startsWith('.') && entry.name !== '.env.example') continue;

      const fullPath = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walkDir(fullPath, callback);
      } else if (entry.isFile()) {
        callback(fullPath);
      }
    }
  } catch (e) {}
}

function buildTree(dir, maxDepth, currentDepth) {
  if (currentDepth >= maxDepth) return null;

  const result = { name: path.basename(dir), type: 'dir', children: [] };

  try {
    const entries = fs.readdirSync(dir, { withFileTypes: true })
      .sort((a, b) => {
        if (a.isDirectory() !== b.isDirectory()) return a.isDirectory() ? -1 : 1;
        return a.name.localeCompare(b.name);
      });

    for (const entry of entries) {
      if (SKIP_DIRS.has(entry.name)) continue;

      const fullPath = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        const subtree = buildTree(fullPath, maxDepth, currentDepth + 1);
        if (subtree) result.children.push(subtree);
      } else {
        result.children.push({ name: entry.name, type: 'file', size: fs.statSync(fullPath).size });
      }
    }
  } catch (e) {}

  return result;
}

function recordPath(filePath) {
  try {
    if (!fs.existsSync(CACHE_DIR)) {
      fs.mkdirSync(CACHE_DIR, { recursive: true });
    }
    const pendingPath = path.join(CACHE_DIR, 'pending-paths.jsonl');
    fs.appendFileSync(pendingPath, JSON.stringify({
      path: filePath,
      timestamp: Date.now()
    }) + '\n');
  } catch (e) {}
}

function globToRegex(pattern) {
  const escaped = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*\*/g, '<<<GLOBSTAR>>>')
    .replace(/\*/g, '[^/]*')
    .replace(/<<<GLOBSTAR>>>/g, '.*')
    .replace(/\?/g, '.');
  return new RegExp(`^${escaped}$`);
}

function output(data) {
  process.stdout.write(JSON.stringify(data, null, 2));
}

function getFlag(flag) {
  const idx = args.indexOf(flag);
  return idx >= 0 && idx < args.length - 1 ? args[idx + 1] : null;
}

function hasFlag(flag) {
  return args.includes(flag);
}
