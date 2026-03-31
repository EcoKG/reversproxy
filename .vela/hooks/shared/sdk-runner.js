/**
 * Vela SDK Runner
 * Wraps @anthropic-ai/claude-agent-sdk `query()` for spawning SDK agents
 * from within Claude Code hooks. CJS module — dynamic ESM import at runtime.
 *
 * Key design decisions:
 * - settingSources: [] — prevents Vela hooks from loading in SDK-spawned agents (hook isolation)
 * - permissionMode: 'bypassPermissions' — SDK agents run under engine control, not interactive
 * - Auth inherited from process.env (ANTHROPIC_API_KEY) — no explicit key handling
 * - Entire invocation wrapped in try/catch — SDK errors never crash the caller
 */

'use strict';

/**
 * Dynamically import the Claude Agent SDK.
 * Returns the module or null if unavailable.
 * @returns {Promise<{query: Function}|null>}
 */
async function loadSdk() {
  try {
    const sdk = await import('@anthropic-ai/claude-agent-sdk');
    return sdk;
  } catch (err) {
    return { _error: err };
  }
}

/**
 * Compute retry delay with exponential backoff, clamped to [1000, 60000]ms.
 * If resetsAt timestamp is available and in the future, uses that instead.
 *
 * @param {number} attempt - Zero-based attempt index
 * @param {number} baseDelayMs - Base delay in milliseconds
 * @param {number|null} resetsAt - Optional Unix timestamp (ms) when rate limit resets
 * @returns {number} Delay in milliseconds
 */
function computeRetryDelay(attempt, baseDelayMs, resetsAt) {
  const MIN_DELAY = 1000;
  const MAX_DELAY = 60000;

  // If resetsAt is available and in the future, use it
  if (resetsAt != null) {
    const waitMs = resetsAt - Date.now();
    if (waitMs > 0) {
      return Math.min(Math.max(waitMs, MIN_DELAY), MAX_DELAY);
    }
  }

  // Exponential backoff: 2^attempt * baseDelayMs
  const delay = Math.pow(2, attempt) * baseDelayMs;
  return Math.min(Math.max(delay, MIN_DELAY), MAX_DELAY);
}

/**
 * Run an SDK agent with the given options.
 *
 * @param {Object} opts
 * @param {string} opts.prompt - Required. The prompt to send to the agent.
 * @param {string} [opts.model] - Model identifier (e.g. 'claude-sonnet-4-5-20250929').
 * @param {string} [opts.cwd] - Working directory for the agent.
 * @param {string[]} [opts.allowedTools] - Tools the agent may use.
 * @param {string[]} [opts.disallowedTools] - Tools the agent may NOT use.
 * @param {number} [opts.maxTurns] - Maximum conversation turns.
 * @param {number} [opts.maxBudgetUsd] - Budget cap in USD.
 * @param {string} [opts.permissionMode='bypassPermissions'] - Permission mode.
 * @param {string|Object} [opts.systemPrompt] - System prompt string or preset object.
 * @param {boolean} [opts.persistSession=false] - Whether to persist the session.
 * @param {AbortController} [opts.abortController] - Optional abort controller.
 * @param {number} [opts.maxRetries=3] - Maximum retry attempts on rate limit errors.
 * @param {number} [opts.retryDelayMs=2000] - Base delay for exponential backoff (ms).
 *
 * @returns {Promise<Object>} Normalized result object:
 *   Success: { ok: true, result, cost, model, sessionId, numTurns, durationMs }
 *   SDK error result: { ok: false, error: subtype, details, cost, numTurns, durationMs, retriesAttempted? }
 *   SDK unavailable: { ok: false, error: 'sdk_not_available', details: errorMessage }
 *   Unexpected error: { ok: false, error: 'unexpected_error', details: errorMessage }
 */
async function runSdkAgent(opts) {
  if (!opts || typeof opts.prompt !== 'string' || opts.prompt.length === 0) {
    return { ok: false, error: 'invalid_input', details: 'prompt is required and must be a non-empty string' };
  }

  // --- Load SDK dynamically ---
  const sdk = await loadSdk();

  if (sdk._error) {
    return {
      ok: false,
      error: 'sdk_not_available',
      details: sdk._error.message || String(sdk._error)
    };
  }

  if (typeof sdk.query !== 'function') {
    return {
      ok: false,
      error: 'sdk_not_available',
      details: 'SDK loaded but query() function not found'
    };
  }

  // --- Build query options ---
  const queryOptions = {
    // Hook isolation: empty settingSources prevents Vela hooks from loading
    // in SDK-spawned agents. Set explicitly — do not rely on SDK defaults.
    settingSources: [],

    // SDK agents run under engine control, not interactive
    permissionMode: opts.permissionMode || 'bypassPermissions',
    allowDangerouslySkipPermissions: true,

    // Ephemeral by default
    persistSession: opts.persistSession === true ? true : false,
  };

  // Optional fields — only set if provided
  if (opts.model) queryOptions.model = opts.model;
  if (opts.cwd) queryOptions.cwd = opts.cwd;
  if (opts.allowedTools) queryOptions.allowedTools = opts.allowedTools;
  if (opts.disallowedTools) queryOptions.disallowedTools = opts.disallowedTools;
  if (opts.maxTurns != null) queryOptions.maxTurns = opts.maxTurns;
  if (opts.maxBudgetUsd != null) queryOptions.maxBudgetUsd = opts.maxBudgetUsd;
  if (opts.systemPrompt != null) queryOptions.systemPrompt = opts.systemPrompt;
  if (opts.abortController) queryOptions.abortController = opts.abortController;

  // --- Rate limit retry parameters ---
  const maxRetries = opts.maxRetries != null ? opts.maxRetries : 3;
  const retryDelayMs = opts.retryDelayMs != null ? opts.retryDelayMs : 2000;

  let totalCost = 0;
  let retriesAttempted = 0;

  // --- Execute query with rate-limit retry loop ---
  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    try {
      const startMs = Date.now();
      const generator = sdk.query({ prompt: opts.prompt, options: queryOptions });

      let resultMessage = null;
      let sessionId = null;
      let sawRateLimit = false;
      let resetsAt = null;

      for await (const message of generator) {
        // Capture session ID from init message
        if (message.type === 'system' && message.subtype === 'init' && message.session_id) {
          sessionId = message.session_id;
        }

        // Detect rate limit events during execution
        if (message.type === 'rate_limit_event' || (message.type === 'system' && message.subtype === 'rate_limit_event')) {
          sawRateLimit = true;
          if (message.resets_at) resetsAt = message.resets_at;
        }

        // Capture the final result message
        if (message.type === 'result') {
          resultMessage = message;
        }
      }

      const elapsedMs = Date.now() - startMs;

      if (!resultMessage) {
        return {
          ok: false,
          error: 'no_result',
          details: 'SDK query completed without producing a result message',
          durationMs: elapsedMs,
          ...(retriesAttempted > 0 ? { retriesAttempted, cost: totalCost } : {})
        };
      }

      // Accumulate cost from this attempt
      if (resultMessage.total_cost_usd != null) {
        totalCost += resultMessage.total_cost_usd;
      }

      // --- Normalize result ---
      if (resultMessage.subtype === 'success') {
        return {
          ok: true,
          result: resultMessage.result,
          cost: totalCost,
          model: resultMessage.model || opts.model || null,
          sessionId: resultMessage.session_id || sessionId,
          numTurns: resultMessage.num_turns,
          durationMs: resultMessage.duration_ms || elapsedMs,
          ...(retriesAttempted > 0 ? { retriesAttempted } : {})
        };
      }

      // --- Rate limit retry logic ---
      // If the result is error_during_execution and we saw a rate limit event,
      // retry with exponential backoff (unless we've exhausted retries).
      if (
        sawRateLimit &&
        resultMessage.subtype === 'error_during_execution' &&
        attempt < maxRetries
      ) {
        retriesAttempted = attempt + 1;
        const delay = computeRetryDelay(attempt, retryDelayMs, resetsAt);
        await new Promise(resolve => setTimeout(resolve, delay));
        continue; // retry
      }

      // Error subtypes: 'error_max_turns', 'error_during_execution', etc.
      const errorDetails = Array.isArray(resultMessage.errors)
        ? resultMessage.errors.join(', ')
        : resultMessage.result || 'Unknown error';

      return {
        ok: false,
        error: resultMessage.subtype || 'unknown_error',
        details: errorDetails,
        cost: totalCost,
        numTurns: resultMessage.num_turns,
        durationMs: resultMessage.duration_ms || elapsedMs,
        ...(retriesAttempted > 0 ? { retriesAttempted } : {})
      };

    } catch (err) {
      return {
        ok: false,
        error: 'unexpected_error',
        details: err.message || String(err),
        ...(retriesAttempted > 0 ? { retriesAttempted, cost: totalCost } : {})
      };
    }
  }
}

module.exports = { runSdkAgent, computeRetryDelay };
