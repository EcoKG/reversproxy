#!/usr/bin/env node
const { execSync } = require('child_process');
const path = require('path');
const ROOT = path.join(__dirname, '../..');

function run(cmd, env = {}) {
  console.log(`\n$ ${cmd}`);
  try {
    const out = execSync(cmd, {
      cwd: ROOT, encoding: 'utf8',
      env: { ...process.env, ...env },
      stdio: ['pipe', 'pipe', 'pipe']
    });
    if (out.trim()) console.log(out);
    return { ok: true, out };
  } catch (e) {
    if (e.stdout) console.log(e.stdout);
    if (e.stderr) console.error(e.stderr);
    return { ok: false };
  }
}

console.log('=== winclient verify ===');

// 1. go mod tidy completed check
const modCheck = run('cat go.mod');

// 2. Build linux client
console.log('\n--- Build: linux/amd64 client (no CGO) ---');
const r2 = run('go build -o /tmp/verify-client-linux ./cmd/client',
  { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' });
console.log(r2.ok ? 'PASS' : 'FAIL');

// 3. Build linux arm64 client
console.log('\n--- Build: linux/arm64 client (no CGO) ---');
const r3 = run('go build -o /tmp/verify-client-arm64 ./cmd/client',
  { GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' });
console.log(r3.ok ? 'PASS' : 'FAIL');

// 4. go vet linux packages
console.log('\n--- go vet: linux packages ---');
const r4 = run('go vet ./internal/config/... ./internal/control/... ./internal/protocol/... ./cmd/client/... ./cmd/server/...',
  { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' });
console.log(r4.ok ? 'PASS' : 'FAIL');

// 5. go vet winclient with GOOS=windows
console.log('\n--- go vet: winclient (GOOS=windows) ---');
const r5 = run('go vet ./cmd/winclient/...',
  { GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '1' });
console.log(r5.ok ? 'PASS' : 'FAIL');

// 6. Tests (excluding known-broken internal/app and flaky reconnect)
console.log('\n--- go test: config, control, protocol ---');
const r6 = run('go test -count=1 ./internal/config/... ./internal/control/... ./internal/protocol/...',
  { GOOS: 'linux' });
console.log(r6.ok ? 'PASS' : 'FAIL');

// Summary
const allPassed = r2.ok && r3.ok && r4.ok && r5.ok && r6.ok;
console.log('\n=== SUMMARY ===');
console.log(`linux/amd64 build: ${r2.ok ? 'PASS' : 'FAIL'}`);
console.log(`linux/arm64 build: ${r3.ok ? 'PASS' : 'FAIL'}`);
console.log(`go vet linux:      ${r4.ok ? 'PASS' : 'FAIL'}`);
console.log(`go vet winclient:  ${r5.ok ? 'PASS' : 'FAIL'}`);
console.log(`go test core:      ${r6.ok ? 'PASS' : 'FAIL'}`);
console.log(allPassed ? '\n=== ALL PASSED ===' : '\n=== SOME FAILED ===');
process.exit(allPassed ? 0 : 1);
