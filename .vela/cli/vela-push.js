#!/usr/bin/env node
/**
 * Vela Push Helper — push branch and create release tag
 * Usage: node .vela/cli/vela-push.js <branch> <tag> [sha]
 */
const { execSync } = require('child_process');

const branch = process.argv[2] || 'HEAD';
const tag = process.argv[3];

function run(cmd) {
  console.log(`$ ${cmd}`);
  try {
    const out = execSync(cmd, { stdio: 'pipe', encoding: 'utf-8' });
    if (out) console.log(out.trim());
    return true;
  } catch (e) {
    console.error(e.stderr || e.message);
    return false;
  }
}

// Push branch
console.log(`\n[1/3] Pushing branch: ${branch}`);
if (!run(`git push origin ${branch}`)) process.exit(1);

if (tag) {
  // Create annotated tag
  console.log(`\n[2/3] Creating tag: ${tag}`);
  run(`git tag -a ${tag} -m "Release ${tag}" 2>/dev/null || git tag -f ${tag}`);

  // Push tag
  console.log(`\n[3/3] Pushing tag: ${tag}`);
  if (!run(`git push origin ${tag}`)) process.exit(1);
}

console.log('\n✅ Done.');
