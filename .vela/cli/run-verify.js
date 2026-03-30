#!/usr/bin/env node
// Verify script: runs go commands for the winclient build verification
const { execSync } = require('child_process');
const path = require('path');

const projectRoot = path.join(__dirname, '../..');

function run(cmd, opts = {}) {
  console.log(`\n$ ${cmd}`);
  try {
    const out = execSync(cmd, {
      cwd: projectRoot,
      encoding: 'utf8',
      stdio: ['pipe', 'pipe', 'pipe'],
      ...opts,
    });
    if (out) console.log(out);
    return { ok: true, stdout: out };
  } catch (e) {
    console.error(e.stdout || '');
    console.error(e.stderr || '');
    return { ok: false, error: e.message, stdout: e.stdout, stderr: e.stderr };
  }
}

console.log('=== Vela Verify: winclient ===\n');

// Step 1: go mod tidy
console.log('--- Step 1: go mod tidy ---');
const tidy = run('go mod tidy');
if (!tidy.ok) { console.error('FAIL: go mod tidy'); process.exit(1); }

// Step 2: go test ./internal/...
console.log('--- Step 2: go test ./internal/... ---');
const tests = run('go test ./internal/...');
if (!tests.ok) { console.error('FAIL: go test'); process.exit(1); }

// Step 3: Build Linux AMD64 client
console.log('--- Step 3: Build linux/amd64 client ---');
const buildLinux = run('go build -o /tmp/test-client-linux ./cmd/client', {
  env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' }
});
if (!buildLinux.ok) { console.error('FAIL: linux build'); process.exit(1); }

// Step 4: go vet ./...  (excluding winclient which needs windows)
console.log('--- Step 4: go vet (linux packages only) ---');
const vet = run('go vet ./internal/... ./cmd/client/... ./cmd/server/...', {
  env: { ...process.env, GOOS: 'linux', CGO_ENABLED: '0' }
});
if (!vet.ok) { console.error('FAIL: go vet'); process.exit(1); }

// Step 5: Verify winclient syntax with GOOS=windows
console.log('--- Step 5: go vet winclient (GOOS=windows) ---');
const vetWin = run('go vet ./cmd/winclient/...', {
  env: { ...process.env, GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '1' }
});
if (!vetWin.ok) { console.error('FAIL: go vet winclient'); process.exit(1); }

console.log('\n=== ALL CHECKS PASSED ===');
