/**
 * Vela Shared Constants
 * Single source of truth for gate-keeper, gate-guard, orchestrator, and tracker hooks.
 */

const CODE_EXTENSIONS = new Set([
  '.js', '.jsx', '.ts', '.tsx', '.mjs', '.cjs',
  '.py', '.pyw',
  '.go',
  '.rs',
  '.java', '.kt', '.scala',
  '.c', '.cpp', '.cc', '.h', '.hpp',
  '.cs',
  '.rb',
  '.php',
  '.swift',
  '.html', '.htm', '.css', '.scss', '.sass', '.less',
  '.vue', '.svelte',
  '.json', '.yaml', '.yml', '.toml',
  '.sql',
  '.sh', '.bash', '.zsh',
  '.tf', '.hcl',
  '.dockerfile', '.containerfile'
]);

const SKIP_PATHS = [
  'node_modules/',
  '.git/',
  'dist/',
  'build/',
  'out/',
  '.next/',
  '.nuxt/',
  'vendor/',
  '__pycache__/',
  '.venv/',
  'venv/',
  '.cache/',
  'coverage/',
  '.vela/cache/',
  'package-lock.json',
  'yarn.lock',
  'pnpm-lock.yaml',
  'Cargo.lock',
  'go.sum',
  'poetry.lock'
];

const SENSITIVE_FILES = [
  '.env',
  '.env.local',
  '.env.production',
  '.env.staging',
  'credentials.json',
  'secrets.json',
  'secrets.yaml',
  '.npmrc',
  '.pypirc',
  'id_rsa',
  'id_ed25519'
];

const WRITE_TOOLS = new Set(['Edit', 'Write', 'NotebookEdit']);
const READ_TOOLS = new Set(['Read', 'Glob', 'Grep']);

const SECRET_PATTERNS = [
  /(?:AKIA|ASIA)[A-Z0-9]{16}/,                    // AWS access key
  /ghp_[A-Za-z0-9_]{36}/,                         // GitHub PAT
  /gho_[A-Za-z0-9_]{36}/,                         // GitHub OAuth
  /sk-[A-Za-z0-9]{48}/,                           // OpenAI key
  /sk-ant-[A-Za-z0-9-]{90,}/,                     // Anthropic key
  /eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\./,  // JWT
  /sk_live_[A-Za-z0-9]{24,}/,                     // Stripe live key
  /rk_live_[A-Za-z0-9]{24,}/,                     // Stripe restricted key
  /mongodb\+srv:\/\/[^:]+:[^@]+@/,                // MongoDB connection
  /postgres(?:ql)?:\/\/[^:]+:[^@]+@/,             // PostgreSQL connection
  /mysql:\/\/[^:]+:[^@]+@/,                       // MySQL connection
  /-----BEGIN (?:RSA |EC )?PRIVATE KEY-----/,      // Private key
  /xox[bpsar]-[A-Za-z0-9-]{10,}/,                 // Slack token
  /AIza[A-Za-z0-9_-]{35}/,                        // Google API key
  /SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}/     // SendGrid key
];

// Bash commands that are safe in read-only mode
const SAFE_BASH_READ = /^\s*(ls|cat|head|tail|find|grep|rg|wc|file|stat|tree|pwd|echo|which|node\s+.*--version|python3?\s+--version|git\s+(status|log|diff|branch|show|blame|remote))\b/;

// Bash commands that write files
const BASH_WRITE_PATTERNS = [
  />\s*\S/,                          // redirect
  /\|\s*tee\s/,                      // pipe to tee
  /\bcp\s/,                          // copy
  /\bmv\s/,                          // move
  /\brm\s/,                          // remove
  /\bmkdir\s/,                       // create dir
  /\btouch\s/,                       // create file
  /\bsed\s+-i/,                      // sed in-place
  /\bchmod\s/,                       // change permissions
  /\bchown\s/,                       // change ownership
  /\bgit\s+(add|commit|push|merge|rebase|reset|checkout|stash)/,
  /\bnpm\s+(install|uninstall|update|publish)/,
  /\byarn\s+(add|remove|install)/,
  /\bpip\s+(install|uninstall)/
];

module.exports = {
  CODE_EXTENSIONS,
  SKIP_PATHS,
  SENSITIVE_FILES,
  WRITE_TOOLS,
  READ_TOOLS,
  SECRET_PATTERNS,
  SAFE_BASH_READ,
  BASH_WRITE_PATTERNS
};
