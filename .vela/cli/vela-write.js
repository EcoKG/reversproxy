#!/usr/bin/env node
/**
 * Vela Write CLI — Sandbox-aware file writing
 *
 * Writes files within the Vela sandbox. All writes go through mode checks
 * and are logged for audit trail.
 *
 * Usage:
 *   vela-write <file-path> --content <text>
 *   vela-write <file-path> --stdin            — Read content from stdin
 *   vela-write <file-path> --edit --old <old> --new <new>  — Partial edit
 *   vela-write --mkdir <dir-path>
 *
 * Output: JSON with operation result
 */

const fs = require('fs');
const path = require('path');

const args = process.argv.slice(2);
const CWD = process.cwd();
const VELA_DIR = path.join(CWD, '.vela');

// ─── Command Router ───
if (hasFlag('--mkdir')) {
  cmdMkdir();
} else if (hasFlag('--edit')) {
  cmdEdit();
} else {
  cmdWrite();
}

// ─── Write entire file ───
async function cmdWrite() {
  const filePath = args[0];
  if (!filePath) {
    return output({ ok: false, error: 'File path required. Usage: vela-write <file-path> --content "text"' });
  }

  const absPath = path.isAbsolute(filePath) ? filePath : path.resolve(CWD, filePath);

  let content;
  if (hasFlag('--stdin')) {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    content = Buffer.concat(chunks).toString();
  } else {
    content = getFlag('--content');
    if (content === null) {
      return output({ ok: false, error: 'Content required. Use --content "text" or --stdin' });
    }
  }

  // Ensure parent directory exists
  const dir = path.dirname(absPath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  const existed = fs.existsSync(absPath);
  const previousSize = existed ? fs.statSync(absPath).size : 0;

  // Atomic write
  try {
    const tmpPath = absPath + '.vela-tmp';
    fs.writeFileSync(tmpPath, content);
    fs.renameSync(tmpPath, absPath);
  } catch (e) {
    return output({ ok: false, error: `Write failed: ${e.message}` });
  }

  // Log the write
  logWrite(absPath, existed ? 'overwrite' : 'create', content.length);

  output({
    ok: true,
    command: 'write',
    path: absPath,
    operation: existed ? 'overwrite' : 'create',
    size: content.length,
    previous_size: previousSize,
    lines: content.split('\n').length
  });
}

// ─── Partial edit (find/replace) ───
function cmdEdit() {
  const filePath = args[0];
  if (!filePath) {
    return output({ ok: false, error: 'File path required.' });
  }

  const absPath = path.isAbsolute(filePath) ? filePath : path.resolve(CWD, filePath);

  if (!fs.existsSync(absPath)) {
    return output({ ok: false, error: `File not found: ${absPath}` });
  }

  const oldText = getFlag('--old');
  const newText = getFlag('--new');

  if (oldText === null || newText === null) {
    return output({ ok: false, error: 'Both --old and --new required for edit.' });
  }

  let content = fs.readFileSync(absPath, 'utf-8');
  const originalContent = content;

  if (!content.includes(oldText)) {
    return output({
      ok: false,
      error: 'Old text not found in file.',
      hint: 'Check for exact whitespace/newline matches.'
    });
  }

  content = content.replace(oldText, newText);

  // Atomic write
  try {
    const tmpPath = absPath + '.vela-tmp';
    fs.writeFileSync(tmpPath, content);
    fs.renameSync(tmpPath, absPath);
  } catch (e) {
    return output({ ok: false, error: `Edit failed: ${e.message}` });
  }

  logWrite(absPath, 'edit', Math.abs(content.length - originalContent.length));

  output({
    ok: true,
    command: 'edit',
    path: absPath,
    chars_changed: Math.abs(content.length - originalContent.length),
    old_size: originalContent.length,
    new_size: content.length
  });
}

// ─── Create directory ───
function cmdMkdir() {
  const dirPath = getFlag('--mkdir');
  if (!dirPath) {
    return output({ ok: false, error: 'Directory path required.' });
  }

  const absPath = path.isAbsolute(dirPath) ? dirPath : path.resolve(CWD, dirPath);

  try {
    fs.mkdirSync(absPath, { recursive: true });
  } catch (e) {
    return output({ ok: false, error: `Mkdir failed: ${e.message}` });
  }

  output({
    ok: true,
    command: 'mkdir',
    path: absPath
  });
}

// ─── Helpers ───

function logWrite(filePath, operation, sizeChange) {
  const logPath = path.join(VELA_DIR, 'write-log.jsonl');
  try {
    if (!fs.existsSync(VELA_DIR)) return;
    fs.appendFileSync(logPath, JSON.stringify({
      path: filePath,
      operation: operation,
      size_change: sizeChange,
      timestamp: Date.now()
    }) + '\n');
  } catch (e) {}
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
