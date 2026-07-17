// `observability` mode -- measures the AI SDK's NATIVE OpenTelemetry emission,
// not a bolted-on logger. Wires an in-process Node OTel SDK
// (`NodeTracerProvider` + `InMemorySpanExporter`, see `../otel.mjs`) and the
// AI SDK's own `@ai-sdk/otel` integration, then runs ONE representative
// multi-op turn -- the same real, executed `calculate` tool loop as `tools`
// mode (2 real agnes chat calls: decide-to-call-tool, then
// finish-after-tool-result, plus one real tool execution) -- so the emitted
// span tree has more than a single flat node to report on.
//
// No external collector, no OTLP network egress for telemetry: the
// InMemorySpanExporter keeps every finished span in a JS array in this
// process only, read back directly after the call completes.

import { generateText, tool, stepCountIs } from 'ai';
import { z } from 'zod';
import { evaluateArithmetic } from '../calculator.mjs';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';
import { startObservability, spanDurationMs, stopObservability } from '../otel.mjs';

const MAX_STEPS = 5;

const DEFAULT_SYSTEM =
  'You are a precise assistant. For ANY arithmetic, call the "calculate" tool instead ' +
  'of computing it yourself -- never guess a numeric result. After you see the tool\'s ' +
  'result, give the final answer in one short sentence.';

function buildCalculateTool() {
  return tool({
    description:
      'Evaluate a real arithmetic expression (+, -, *, /, parentheses, unary +/-) and ' +
      'return its exact numeric result.',
    inputSchema: z.object({
      expression: z
        .string()
        .describe('An arithmetic expression, e.g. "(23847 * 9182) / 17"'),
    }),
    execute: async ({ expression }) => {
      const value = evaluateArithmetic(expression);
      return { expression, result: value };
    },
  });
}

/**
 * Build the `span_timings_ms` map keyed by span name. When the same span name
 * occurs more than once (e.g. two `chat {modelId}` spans, one per real agnes
 * call in the tool loop), later occurrences get a ` #<index>` suffix so no
 * timing is silently overwritten -- every finished span is represented.
 */
function buildSpanTimings(spans) {
  const nameCounts = new Map();
  const timings = {};
  for (const span of spans) {
    const seen = (nameCounts.get(span.name) || 0) + 1;
    nameCounts.set(span.name, seen);
    const key = seen > 1 ? `${span.name} #${seen}` : span.name;
    timings[key] = Number(spanDurationMs(span).toFixed(3));
  }
  return timings;
}

export async function runObservability(input, env) {
  console.error('PROGRESS: observability -- starting in-process OTel SDK (NodeTracerProvider + InMemorySpanExporter)');
  const { provider, exporter } = startObservability();

  const messages = buildMessages(input.messages);
  const system = typeof input.system === 'string' && input.system.length > 0
    ? input.system
    : DEFAULT_SYSTEM;

  console.error('PROGRESS: observability -- running representative multi-op turn (real tool loop, experimental_telemetry enabled)');
  const startedAt = process.hrtime.bigint();
  const result = await generateText({
    model: env.chatModel,
    system,
    messages,
    tools: { calculate: buildCalculateTool() },
    stopWhen: stepCountIs(MAX_STEPS),
    experimental_telemetry: { isEnabled: true, functionId: 'arena-observability-tools-turn' },
  });
  const latency_ms = msSince(startedAt);

  console.error('PROGRESS: observability -- flushing span processor and reading InMemorySpanExporter');
  const spans = exporter.getFinishedSpans();
  const span_names = spans.map((s) => s.name);
  const span_timings_ms = buildSpanTimings(spans);

  const sampleSpan = spans.find((s) => s.name.startsWith('chat')) || spans[0] || null;
  const log_sample = sampleSpan
    ? JSON.stringify({
        name: sampleSpan.name,
        kind: sampleSpan.kind,
        attributes: sampleSpan.attributes,
      })
    : '';

  await stopObservability(provider);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const resultsByCallId = new Map((result.toolResults || []).map((r) => [r.toolCallId, r]));
  const tool_calls = (result.toolCalls || []).map((call) => ({
    name: call.toolName,
    arguments: call.input !== undefined ? call.input : {},
    output: resultsByCallId.has(call.toolCallId) ? resultsByCallId.get(call.toolCallId).output : null,
  }));

  console.error(`PROGRESS: observability -- span_count=${spans.length} names=${JSON.stringify(span_names)}`);

  const notes =
    'AI SDK 7.0.31 does NOT auto-emit OTel spans from `experimental_telemetry: { isEnabled: true }` ' +
    'alone -- that flag only opts a given call INTO telemetry collection (or, set to false, opts it ' +
    'out). A `Telemetry` integration must still be registered globally first via `registerTelemetry()`: ' +
    'this mode installs the separate `@ai-sdk/otel` package and calls `registerTelemetry(new ' +
    'OpenTelemetry())` once at process start. Once registered, every generateText/streamText/embed ' +
    'call in-process emits spans automatically, with no explicit `experimental_telemetry` needed per ' +
    'call. Spans follow the OTel GenAI semantic conventions (gen_ai.* attributes): a root ' +
    '"invoke_agent {modelId}" span (INTERNAL, covers all steps), one "chat {modelId}" span per real ' +
    'provider call (CLIENT; 2 here -- decide-to-call-tool, then finish-after-tool-result), and one ' +
    '"execute_tool calculate" span (INTERNAL) nested under the step that ran the tool. Per-span ' +
    'wall-clock timing is native (span.duration, read here via ReadableSpan) -- no manual ' +
    'instrumentation needed. There is no separate "structured logging" facility in the AI SDK itself: ' +
    'the closest equivalent is the span attribute payload, which is fully structured key/value JSON ' +
    '(gen_ai.usage.input_tokens/output_tokens, gen_ai.request.model, gen_ai.tool.call.arguments/' +
    'result, gen_ai.response.finish_reasons, ...) rather than a plain-text log line -- `log_sample` ' +
    'below is that attribute payload for one span, not a console log.';

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls,
    otel_supported: spans.length > 0,
    span_count: spans.length,
    span_names,
    span_timings_ms,
    logs_structured: true,
    log_sample,
    notes,
  };
}
