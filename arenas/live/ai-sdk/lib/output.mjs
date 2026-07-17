// Shared output-shape + small utility helpers used by every mode handler, so the
// stdout contract stays identical no matter which mode ran.

/**
 * Build the base output object. Every code path (success or error), in every mode,
 * fills in this shape so stdout always carries exactly one valid JSON object.
 *
 * Fields beyond the original single-shot contract (`mode`, `retrieved`, `structured`,
 * `ttft_ms`) are additive: the shared arena runner (arenas/live/runner) decodes JSON
 * into a Go struct that silently ignores unknown fields, so this stays backward
 * compatible with the existing `qa`-only task suite.
 */
export function baseOutput(mode, modelName) {
  return {
    framework: 'ai-sdk',
    mode: mode || 'qa',
    model: modelName || '',
    reply: '',
    input_tokens: -1,
    output_tokens: -1,
    latency_ms: -1,
    tool_calls: [],
    retrieved: [],
    structured: null,
    ttft_ms: -1,
    error: '',
  };
}

/**
 * Extract input/output token counts from an AI SDK `usage` object, tolerating both
 * the v5+/v7 naming (usage.inputTokens / usage.outputTokens) and the older v4 naming
 * (usage.promptTokens / usage.completionTokens). Falls back to -1 when a field is
 * missing or not a finite number, per the adapter contract (never fabricate a count).
 */
export function extractTokenCounts(usage) {
  if (!usage || typeof usage !== 'object') {
    return { input_tokens: -1, output_tokens: -1 };
  }

  const inputCandidate =
    usage.inputTokens !== undefined ? usage.inputTokens : usage.promptTokens;
  const outputCandidate =
    usage.outputTokens !== undefined ? usage.outputTokens : usage.completionTokens;

  const input_tokens = Number.isFinite(inputCandidate) ? inputCandidate : -1;
  const output_tokens = Number.isFinite(outputCandidate) ? outputCandidate : -1;

  return { input_tokens, output_tokens };
}

/**
 * Normalize the adapter's input `messages` into the {role, content} shape every
 * mode passes to the AI SDK.
 */
export function buildMessages(messages) {
  if (!Array.isArray(messages)) {
    return [];
  }
  return messages
    .filter((m) => m && typeof m.role === 'string' && typeof m.content === 'string')
    .map((m) => ({ role: m.role, content: m.content }));
}

/**
 * The most recent `user` message's content, or null. Used by modes (rag) that
 * treat the final user turn as "the query".
 */
export function lastUserContent(messages) {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    if (messages[i].role === 'user') {
      return messages[i].content;
    }
  }
  return null;
}

/** Milliseconds elapsed since a `process.hrtime.bigint()` mark, as a plain Number. */
export function msSince(startedAt) {
  const endedAt = process.hrtime.bigint();
  return Number((endedAt - startedAt) / 1000000n);
}
