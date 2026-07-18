// Shared output-shape + small utility helpers used by every mode handler, so the
// stdout contract stays identical no matter which mode ran. Deliberately kept
// byte-for-byte compatible with the other TypeScript arena adapters (ai-sdk,
// llamaindex, ...): the shared Go runner (arenas/live/runner) decodes stdout into
// a struct that silently ignores unknown fields, so extra fields are additive and
// the `framework` label is the only per-adapter difference here.

/**
 * Build the base output object. Every code path (success or error), in every mode,
 * fills in this shape so stdout always carries exactly one valid JSON object.
 */
export function baseOutput(mode, modelName) {
  return {
    framework: 'mastra',
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
    recalled: '',
    scoped_recall: null,
    leak_detected: null,
    attacker_visible_facts: [],
    otel_supported: null,
    span_count: 0,
    span_names: [],
    span_timings_ms: {},
    logs_structured: null,
    log_sample: '',
    notes: '',
    resumed: null,
    state_lost: null,
    precrash_fact_recalled: null,
    text: '',
    middleware: null,
    error: '',
  };
}

/**
 * Extract input/output token counts from a Mastra `usage` object. Mastra (via the
 * Vercel AI SDK v7 under the hood) reports `usage.inputTokens` / `usage.outputTokens`;
 * this also tolerates the older `promptTokens` / `completionTokens` naming. Falls back
 * to -1 when a field is missing or not a finite number (never fabricate a count).
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
 * Normalize a Mastra generate/stream result's tool calls (shape:
 * `{ type, runId, from, payload: { toolCallId, toolName, args } }`) plus optional
 * matching tool results (`{ payload: { toolCallId, result } }`) into the uniform
 * `tool_calls` contract shape `{ name, arguments, output? }`.
 */
export function mapToolCalls(toolCalls, toolResults) {
  const calls = Array.isArray(toolCalls) ? toolCalls : [];
  const results = Array.isArray(toolResults) ? toolResults : [];
  const resultsByCallId = new Map(
    results.map((r) => {
      const p = r && r.payload ? r.payload : r || {};
      return [p.toolCallId, p.result];
    })
  );
  return calls.map((call) => {
    const p = call && call.payload ? call.payload : call || {};
    const entry = {
      name: p.toolName !== undefined ? p.toolName : call.toolName,
      arguments: p.args !== undefined ? p.args : call.args !== undefined ? call.args : {},
    };
    if (resultsByCallId.has(p.toolCallId)) {
      entry.output = resultsByCallId.get(p.toolCallId);
    }
    return entry;
  });
}

/**
 * Normalize the adapter's input `messages` into the {role, content} shape every
 * mode passes to a Mastra Agent.
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
