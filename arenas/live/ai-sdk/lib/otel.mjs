// In-process OpenTelemetry wiring for the `observability` mode -- an
// InMemorySpanExporter, NOT an external collector/backend. No OTLP network
// egress: every finished span is kept in a JS array inside this process only.
//
// `@opentelemetry/sdk-trace-node`'s `NodeTracerProvider` (not the bare
// `BasicTracerProvider` from `sdk-trace-base`) is used deliberately:
// `.register()` installs an `AsyncHooksContextManager` by default, which is
// what makes span *nesting* (root `invoke_agent` -> `chat` -> `execute_tool`)
// survive `await` boundaries inside the AI SDK's internal tracer calls. With
// the no-op default context manager (what you get if you skip registration),
// spans would still be captured by the exporter but would not carry correct
// parent/child linkage.
//
// `@ai-sdk/otel`'s `OpenTelemetry` integration resolves its tracer via
// `trace.getTracer("gen_ai")` from `@opentelemetry/api` -- a `ProxyTracer`
// that lazily delegates to whichever `TracerProvider` is globally registered
// at span-creation time, so provider registration and integration
// registration are safe in either order; this module still does provider
// registration first for readability.

import { NodeTracerProvider, InMemorySpanExporter, SimpleSpanProcessor } from '@opentelemetry/sdk-trace-node';
import { registerTelemetry } from 'ai';
import { OpenTelemetry } from '@ai-sdk/otel';

/**
 * Starts a fresh, in-process NodeTracerProvider backed by an
 * InMemorySpanExporter via a SimpleSpanProcessor (synchronous export on every
 * `span.end()` -- no batching delay, no risk of losing spans if the process
 * exits right after the call this mode measures), registers it as the global
 * OTel provider, and registers the AI SDK's own `@ai-sdk/otel` `OpenTelemetry`
 * integration so every subsequent generateText/streamText/embed call in this
 * process emits GenAI-semantic-convention spans automatically.
 */
export function startObservability() {
  const exporter = new InMemorySpanExporter();
  const provider = new NodeTracerProvider({
    spanProcessors: [new SimpleSpanProcessor(exporter)],
  });
  provider.register();

  registerTelemetry(new OpenTelemetry());

  return { provider, exporter };
}

/** OTel `HrTime` tuple `[seconds, nanoseconds]` -> plain milliseconds `Number`. */
function hrTimeToMs([seconds, nanoseconds]) {
  return seconds * 1000 + nanoseconds / 1e6;
}

/** Wall-clock duration of a finished `ReadableSpan`, in milliseconds. */
export function spanDurationMs(span) {
  return hrTimeToMs(span.duration);
}

/** Flush any pending spans, then shut the provider (and its processors) down. */
export async function stopObservability(provider) {
  await provider.forceFlush();
  await provider.shutdown();
}
