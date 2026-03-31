#!/usr/bin/env node
/**
 * Vela Async Test Runner — PostToolUse Hook (async: true)
 *
 * Runs related tests in the background after Write/Edit operations.
 * Since this is an async hook, Claude does not wait for it to finish.
 * Results are delivered via systemMessage JSON on stdout, appearing
 * in the next conversation turn.
 *
 * Flow: stdin JSON → extract file_path → find related tests → run → systemMessage
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');
const { CODE_EXTENSIONS } = require('./shared/constants');

// Extensions that are "code" but shouldn't trigger test runs
// (config/data files rarely have dedicated test suites)
const NON_TESTABLE_EXTENSIONS = new Set([
  '.json', '.yaml', '.yml', '.toml',
  '.sql', '.tf', '.hcl',
  '.dockerfile', '.containerfile',
  '.html', '.htm', '.css', '.scss', '.sass', '.less'
]);

// Test file patterns recognized by this runner
const TEST_FILE_PATTERNS = [
  /\.test\.(js|ts|jsx|tsx|mjs|cjs)$/,
  /\.spec\.(js|ts|jsx|tsx|mjs|cjs)$/
];

const SHELL_TEST_PATTERN = /^test-.*\.sh$/;

const EXEC_TIMEOUT_MS = 90_000; // 90 seconds

async function main() {
  // ─── Parse stdin JSON ───
  let input;
  try {
    const chunks = [];
    for await (const chunk of process.stdin) chunks.push(chunk);
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch (e) {
    process.exit(0);
  }

  const { tool_name, tool_input, cwd } = input;
  if (!tool_name || !tool_input || !cwd) process.exit(0);

  // ─── Only respond to Write/Edit tools ───
  if (tool_name !== 'Write' && tool_name !== 'Edit') process.exit(0);

  // ─── Extract file path ───
  const filePath = tool_input.file_path;
  if (!filePath) process.exit(0);

  // ─── Code file filter ───
  const ext = path.extname(filePath).toLowerCase();
  if (!CODE_EXTENSIONS.has(ext)) process.exit(0);
  if (NON_TESTABLE_EXTENSIONS.has(ext)) process.exit(0);

  // ─── Resolve absolute path ───
  const absFilePath = path.isAbsolute(filePath) ? filePath : path.join(cwd, filePath);
  const fileDir = path.dirname(absFilePath);
  const baseName = path.basename(filePath);
  const nameWithoutExt = baseName.replace(/\.[^.]+$/, '');

  // ─── Find related test files ───
  const testFiles = findRelatedTests(fileDir, nameWithoutExt, cwd);

  if (testFiles.length > 0) {
    // Run specific test files
    const result = runTestFiles(testFiles, cwd);
    emitSystemMessage(filePath, result);
  } else {
    // Fallback: check if project has npm test script
    const hasNpmTest = checkNpmTestScript(cwd);
    if (hasNpmTest) {
      const result = runNpmTest(cwd);
      emitSystemMessage(filePath, result);
    }
    // No tests found at all — silent exit
  }

  process.exit(0);
}

/**
 * Search for test files related to the given source file.
 * Strategy (conservative):
 *   1. Same directory: *.test.{js,ts,...}, *.spec.{js,ts,...}
 *   2. Same directory __tests__/ subdirectory
 *   3. Same directory tests/ subdirectory (test-*.sh)
 *   4. Filename-based matching: utils.js → utils.test.js, utils.spec.js
 */
function findRelatedTests(fileDir, nameWithoutExt, cwd) {
  const found = [];

  // Skip if directory doesn't exist (e.g. file hasn't been created yet)
  if (!fs.existsSync(fileDir)) return found;

  try {
    // 1. Scan same directory for test/spec files
    const dirEntries = fs.readdirSync(fileDir);
    for (const entry of dirEntries) {
      if (isTestFile(entry)) {
        found.push(path.join(fileDir, entry));
      }
    }

    // 2. Scan __tests__/ subdirectory
    const testsSubDir = path.join(fileDir, '__tests__');
    if (fs.existsSync(testsSubDir) && fs.statSync(testsSubDir).isDirectory()) {
      const subEntries = fs.readdirSync(testsSubDir);
      for (const entry of subEntries) {
        if (isTestFile(entry)) {
          found.push(path.join(testsSubDir, entry));
        }
      }
    }

    // 3. Scan tests/ subdirectory for shell test scripts
    const shellTestDir = path.join(fileDir, 'tests');
    if (fs.existsSync(shellTestDir) && fs.statSync(shellTestDir).isDirectory()) {
      const shellEntries = fs.readdirSync(shellTestDir);
      for (const entry of shellEntries) {
        if (SHELL_TEST_PATTERN.test(entry)) {
          found.push(path.join(shellTestDir, entry));
        }
      }
    }
  } catch (e) {
    // Directory read failures are non-fatal
  }

  // 4. Filter to filename-based matches if we found any general tests
  // Prefer exact name matches (utils.js → utils.test.js) over all tests in the dir
  if (found.length > 0) {
    const nameMatches = found.filter(f => {
      const testBaseName = path.basename(f);
      return (
        testBaseName.startsWith(nameWithoutExt + '.test.') ||
        testBaseName.startsWith(nameWithoutExt + '.spec.') ||
        testBaseName === `test-${nameWithoutExt}.sh`
      );
    });
    // If we have exact name matches, prefer those; otherwise use all found
    if (nameMatches.length > 0) return nameMatches;
  }

  return found;
}

/**
 * Check if a filename looks like a test file.
 */
function isTestFile(filename) {
  return TEST_FILE_PATTERNS.some(p => p.test(filename));
}

/**
 * Run specific test files and return { passed, summary }.
 */
function runTestFiles(testFiles, cwd) {
  const jsTests = testFiles.filter(f => !f.endsWith('.sh'));
  const shTests = testFiles.filter(f => f.endsWith('.sh'));
  const results = [];

  // Run JS/TS tests via npx jest (or direct node for simple scripts)
  if (jsTests.length > 0) {
    // Try jest first, fallback to vitest, fallback to direct node execution
    const runners = [
      `npx --no-install jest ${jsTests.map(f => `"${f}"`).join(' ')} --no-coverage --forceExit 2>&1`,
      `npx --no-install vitest run ${jsTests.map(f => `"${f}"`).join(' ')} 2>&1`
    ];

    let ran = false;
    for (const cmd of runners) {
      try {
        const output = execSync(cmd, {
          cwd,
          timeout: EXEC_TIMEOUT_MS,
          encoding: 'utf-8',
          stdio: ['pipe', 'pipe', 'pipe']
        });
        results.push({ passed: true, output: truncate(output, 500) });
        ran = true;
        break;
      } catch (e) {
        // execSync throws on non-zero exit
        if (e.status !== undefined) {
          // Command ran but tests failed
          const output = (e.stdout || '') + (e.stderr || '');
          results.push({ passed: false, output: truncate(output, 500) });
          ran = true;
          break;
        }
        // Command not found — try next runner
      }
    }

    if (!ran) {
      // No test runner available, try direct node execution on each
      for (const testFile of jsTests) {
        try {
          const output = execSync(`node "${testFile}" 2>&1`, {
            cwd,
            timeout: EXEC_TIMEOUT_MS,
            encoding: 'utf-8',
            stdio: ['pipe', 'pipe', 'pipe']
          });
          results.push({ passed: true, output: truncate(output, 500) });
        } catch (e) {
          const output = (e.stdout || '') + (e.stderr || '');
          results.push({ passed: false, output: truncate(output, 500) });
        }
      }
    }
  }

  // Run shell test scripts
  for (const shTest of shTests) {
    try {
      const output = execSync(`bash "${shTest}" 2>&1`, {
        cwd,
        timeout: EXEC_TIMEOUT_MS,
        encoding: 'utf-8',
        stdio: ['pipe', 'pipe', 'pipe']
      });
      results.push({ passed: true, output: truncate(output, 500) });
    } catch (e) {
      const output = (e.stdout || '') + (e.stderr || '');
      results.push({ passed: false, output: truncate(output, 500) });
    }
  }

  const allPassed = results.every(r => r.passed);
  const summary = results.map(r => r.output).join('\n---\n');
  return { passed: allPassed, summary: truncate(summary, 500) };
}

/**
 * Check if the project's package.json has a test script.
 */
function checkNpmTestScript(cwd) {
  try {
    const pkgPath = path.join(cwd, 'package.json');
    if (!fs.existsSync(pkgPath)) return false;
    const pkg = JSON.parse(fs.readFileSync(pkgPath, 'utf-8'));
    return !!(pkg.scripts && pkg.scripts.test && !pkg.scripts.test.includes('no test specified'));
  } catch (e) {
    return false;
  }
}

/**
 * Run npm test as a fallback.
 */
function runNpmTest(cwd) {
  try {
    const output = execSync('npm test 2>&1', {
      cwd,
      timeout: EXEC_TIMEOUT_MS,
      encoding: 'utf-8',
      stdio: ['pipe', 'pipe', 'pipe']
    });
    return { passed: true, summary: truncate(output, 500) };
  } catch (e) {
    const output = (e.stdout || '') + (e.stderr || '');
    return { passed: false, summary: truncate(output, 500) };
  }
}

/**
 * Emit the systemMessage JSON to stdout for Claude to pick up.
 */
function emitSystemMessage(filePath, result) {
  const status = result.passed ? 'PASS ✅' : 'FAIL ❌';
  const shortPath = filePath.length > 60 ? '...' + filePath.slice(-57) : filePath;
  const message = `🧪 [Vela] Background test result for ${shortPath}: ${status}\n${result.summary}`;

  const payload = JSON.stringify({ systemMessage: message });
  process.stdout.write(payload);
}

/**
 * Truncate a string to maxLen characters.
 */
function truncate(str, maxLen) {
  if (!str) return '';
  if (str.length <= maxLen) return str;
  return str.slice(0, maxLen - 3) + '...';
}

/**
 * Escape special regex characters in a string.
 */
function escapeRegex(str) {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// ─── Entry point with full crash guard (K004) ───
main().catch(() => process.exit(0));
