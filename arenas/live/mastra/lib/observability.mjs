// In-process wiring for Mastra's NATIVE observability (its "AI Tracing" system),
// backing the `observability` mode.
//
// KEY DIFFERENTIATOR: unlike frameworks that need an external OTel bridge (e.g. a
// separate `@ai-sdk/otel` integration, or a LangGraph/eino tracer shim), Mastra
// ships a first-class, GenAI-semantic tracing system in `@mastra/observability`.
// It emits a structured span TREE (agent_run -> model_generation -> model_step ->
// model_inference -> tool_call / model_chunk) plus native METRICS (token counts,
// per-op durations) and a structured-log channel -- all through one config on the
// `Mastra` instance, no code-level per-call instrumentation.
//
// This module keeps everything IN-PROCESS: the `TestExporter` from
// `@mastra/observability` is an in-memory span/metric/log sink (NOT an external
// collector, NOT OTLP network egress). Every finished span stays in a JS array in
// this process and is read back directly after the turn completes.

import { Observability, TestExporter } from '@mastra/observability';

/**
 * Create a fresh in-memory observability instance. Returns both the `Observability`
 * object (to hand to `new Mastra({ observability })`) and the underlying
 * `TestExporter` (to read captured spans/metrics/logs back out afterwards).
 */
export function startObservability() {
  // logMetricsOnFlush: false -- the TestExporter otherwise logs a multi-line
  // flush summary via its logger to STDOUT, which would corrupt the adapter's
  // one-JSON-object stdout contract. Spans are captured on SPAN_ENDED events
  // regardless (getCompletedSpans works without any flush), so silencing the
  // flush summary loses nothing.
  const exporter = new TestExporter({ logMetricsOnFlush: false });
  const observability = new Observability({
    configs: {
      arena: {
        serviceName: 'arena-mastra',
        exporters: [exporter],
      },
    },
  });
  return { observability, exporter };
}

/**
 * Wall-clock duration of a Mastra span, in milliseconds, from its ISO/Date
 * start/end timestamps. Returns 0 for an unfinished or malformed span rather than
 * NaN.
 */
export function spanDurationMs(span) {
  if (!span || !span.startTime || !span.endTime) {
    return 0;
  }
  const start = new Date(span.startTime).getTime();
  const end = new Date(span.endTime).getTime();
  const dur = end - start;
  return Number.isFinite(dur) && dur >= 0 ? dur : 0;
}

/**
 * Build a `span_timings_ms` map keyed by a human-readable span label. When two
 * spans share a name (e.g. two `inference: N` spans, one per real agnes call in
 * the tool loop), later occurrences get a ` #<index>` suffix so no timing is
 * silently overwritten -- every finished span is represented.
 */
export function buildSpanTimings(spans) {
  const nameCounts = new Map();
  const timings = {};
  for (const span of spans) {
    const label = span.name || span.type || 'span';
    const seen = (nameCounts.get(label) || 0) + 1;
    nameCounts.set(label, seen);
    const key = seen > 1 ? `${label} #${seen}` : label;
    timings[key] = Number(spanDurationMs(span).toFixed(3));
  }
  return timings;
}

/** Flush the exporter (best-effort) so buffered spans are materialized. */
export async function flushObservability(exporter) {
  if (exporter && typeof exporter.flush === 'function') {
    try {
      await exporter.flush();
    } catch {
      // flush is best-effort: TestExporter already retains spans in memory
      // regardless, so a flush error must not mask a successful capture.
    }
  }
}
