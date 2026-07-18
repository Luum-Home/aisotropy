// `observability` mode -- measures Mastra's NATIVE observability ("AI Tracing"),
// which is Mastra's STRENGTH and a differentiator. Unlike frameworks that need an
// external OTel bridge bolted on (a separate @ai-sdk/otel integration for the
// Vercel AI SDK, or a tracer shim for langgraph/eino), Mastra ships a first-class,
// GenAI-semantic tracing system in `@mastra/observability`: one config on the
// `Mastra` instance and every agent turn emits a structured span TREE plus native
// METRICS (token counts, per-op durations) and a structured-log channel -- with NO
// per-call code instrumentation.
//
// Everything stays IN-PROCESS: the `TestExporter` (lib/observability.mjs) is an
// in-memory span/metric/log sink, NOT an external collector and NOT OTLP network
// egress. This mode runs ONE representative multi-op turn -- the same real,
// executed `calculate` tool loop as `tools` mode (2 real agnes chat calls plus one
// real tool execution) -- so the emitted span tree has more than a single flat
// node to report on, then reads the finished spans straight back out.

import { Mastra } from '@mastra/core';
import { Agent } from '@mastra/core/agent';
import { buildCalculateTool, CALCULATE_SYSTEM } from '../calculate-tool.mjs';
import { buildMessages, extractTokenCounts, mapToolCalls, msSince } from '../output.mjs';
import {
  startObservability,
  buildSpanTimings,
  flushObservability,
} from '../observability.mjs';

const MAX_STEPS = 5;

function spanLogSample(span) {
  if (!span) return '';
  const payload = {
    type: span.type,
    name: span.name,
    traceId: span.traceId,
    spanId: span.id || span.spanId,
  };
  if (span.attributes && typeof span.attributes === 'object') {
    payload.attributes = span.attributes;
  } else if (span.metadata && typeof span.metadata === 'object') {
    payload.metadata = span.metadata;
  }
  if (span.input !== undefined) payload.input = span.input;
  if (span.output !== undefined) payload.output = span.output;
  return JSON.stringify(payload);
}

export async function runObservability(input, env) {
  console.error(
    'PROGRESS: observability -- starting Mastra native AI-tracing (@mastra/observability TestExporter, in-memory, no external collector)'
  );
  const { observability, exporter } = startObservability();

  const instructions =
    typeof input.system === 'string' && input.system.length > 0 ? input.system : CALCULATE_SYSTEM;
  const messages = buildMessages(input.messages);

  const agent = new Agent({
    name: 'arena-observability',
    instructions,
    model: env.chatModel,
    tools: { calculate: buildCalculateTool() },
  });
  // The agent MUST run through a Mastra instance for observability context to
  // propagate -- tracing is a first-class Mastra-instance concern, not an
  // agent-local one.
  const mastra = new Mastra({ agents: { observability: agent }, observability });
  const registeredAgent = mastra.getAgent('observability');

  console.error(
    'PROGRESS: observability -- running representative multi-op turn (real executed tool loop)'
  );
  const startedAt = process.hrtime.bigint();
  const result = await registeredAgent.generate(messages, { maxSteps: MAX_STEPS });
  const latency_ms = msSince(startedAt);

  console.error('PROGRESS: observability -- flushing exporter and reading captured spans');
  await flushObservability(exporter);
  // Small settle so any span_ended events still on the microtask queue are drained.
  await new Promise((r) => setTimeout(r, 200));

  const spans =
    (typeof exporter.getCompletedSpans === 'function' && exporter.getCompletedSpans()) ||
    (typeof exporter.getAllSpans === 'function' && exporter.getAllSpans()) ||
    [];
  const span_names = spans.map((s) => s.name || s.type || 'span');
  const span_timings_ms = buildSpanTimings(spans);

  const stats =
    typeof exporter.getStatistics === 'function' ? exporter.getStatistics() : {};
  const metricNames = stats && stats.metricsByName ? Object.keys(stats.metricsByName) : [];

  // Prefer a model_generation or agent_run span for the log sample (the richest).
  const sampleSpan =
    spans.find((s) => s.type === 'model_generation') ||
    spans.find((s) => s.type === 'agent_run') ||
    spans[0] ||
    null;
  const log_sample = spanLogSample(sampleSpan);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  console.error(
    `PROGRESS: observability -- span_count=${spans.length} types=${JSON.stringify(
      stats.bySpanType || {}
    )} metrics=${JSON.stringify(metricNames)}`
  );

  const notes =
    'Mastra ships NATIVE observability ("AI Tracing") in the separate @mastra/observability ' +
    'package -- enabled by one `observability` config on the `Mastra` instance, no per-call ' +
    'instrumentation and NO external OTel bridge (contrast langgraph/eino, which need a tracer ' +
    'shim, and the Vercel AI SDK, which needs a separate @ai-sdk/otel integration). A single ' +
    'agent turn emits a structured span TREE following GenAI semantics: a root `agent_run` span, ' +
    'a `model_generation` span, one `model_step`+`model_inference` pair per real provider call ' +
    '(2 here: decide-to-call-tool, then finish-after-tool-result), a `tool_call` span for the ' +
    'executed `calculate` tool, and `model_chunk` spans for streamed deltas. Beyond spans, Mastra ' +
    'natively emits METRICS in the SAME pass (mastra_model_total_input_tokens/output_tokens, ' +
    'mastra_model_duration_ms, mastra_tool_duration_ms, mastra_agent_duration_ms) -- captured here ' +
    'as metricsByName=' +
    JSON.stringify(metricNames) +
    '. Per-span wall-clock timing is native (span.startTime/endTime). Span attribute payloads are ' +
    'fully structured key/value JSON, so `logs_structured` is true; `log_sample` below is one ' +
    "span's structured payload. This mode captures all of it IN-PROCESS via the TestExporter (an " +
    'in-memory sink), so there is zero OTLP network egress for telemetry.';

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls: mapToolCalls(result.toolCalls, result.toolResults),
    otel_supported: spans.length > 0,
    span_count: spans.length,
    span_names,
    span_timings_ms,
    logs_structured: true,
    log_sample,
    notes,
  };
}
