#!/usr/bin/env node
// Live arena adapter: Vercel AI SDK (TypeScript/Node ecosystem, run here as plain ESM JS).
//
// EXHAUSTIVE multi-mode adapter (see README.md for the full contract). Every mode
// exercises a different AI SDK NATIVE feature against REAL infrastructure -- no
// mocks, no stubs:
//   qa         - single-shot generateText() (the original behavior, unchanged)
//   rag        - embed() + real Qdrant search + generateText() grounded in context
//   tools      - generateText() with a real executed `calculate` tool + stopWhen
//   memory     - generateText() with history persisted to real rqlite across runs
//   structured - generateObject() against a zod schema
//   stream     - streamText() with real streaming + time-to-first-token
//   memory_scoped - generateText() with history persisted to real rqlite under a
//                HAND-ROLLED compound scope (tenant_id/user_id/product_id[/session_id]);
//                proves scoped recall + cross-tenant miss, not just session persistence
//   isolation_leak - plants a secret under one scope, recalls under another, and
//                proves (or disproves) a cross-tenant leak via a real agnes turn
//   observability - wires an in-process OTel SDK (@ai-sdk/otel + InMemorySpanExporter)
//                and runs a real multi-op tool-loop turn to measure the AI SDK's
//                NATIVE OpenTelemetry span emission (names, counts, per-span timings)
//   crash_recovery - proves survival of a HARD SIGKILL mid-conversation (not just a
//                clean exit like `memory`): "role":"run" checkpoints turns to rqlite
//                one by one; "role":"resume" is a completely fresh process that loads
//                whatever committed and continues, recalling the pre-crash fact
//
// Contract:
//   stdin  -> ONE JSON object, always including "mode" (defaults to "qa" when
//             absent, so the original single-shot arena task suite still works
//             unchanged against this adapter).
//   stdout -> exactly ONE JSON object (the adapter result). ALL logging goes to
//             stderr, prefixed "PROGRESS:" by convention.

import { resolveEnv } from './lib/env.mjs';
import { baseOutput } from './lib/output.mjs';
import { runQa } from './lib/modes/qa.mjs';
import { runRag } from './lib/modes/rag.mjs';
import { runTools } from './lib/modes/tools.mjs';
import { runMemory } from './lib/modes/memory.mjs';
import { runStructured } from './lib/modes/structured.mjs';
import { runStream } from './lib/modes/stream.mjs';
import { runMemoryScoped } from './lib/modes/memory-scoped.mjs';
import { runIsolationLeak } from './lib/modes/isolation-leak.mjs';
import { runObservability } from './lib/modes/observability.mjs';
import { runCrashRecovery } from './lib/modes/crash-recovery.mjs';

const MODE_HANDLERS = {
  qa: runQa,
  rag: runRag,
  tools: runTools,
  memory: runMemory,
  structured: runStructured,
  stream: runStream,
  memory_scoped: runMemoryScoped,
  isolation_leak: runIsolationLeak,
  observability: runObservability,
  crash_recovery: runCrashRecovery,
};

/**
 * Read the entire stdin stream and return it as a UTF-8 string.
 */
async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

async function main() {
  // Resolve OPENAI_MODEL early (even before full env validation) so the output
  // object's `model` field is populated on every path, including early failures.
  const modelNameForOutput = process.env.OPENAI_MODEL || '';

  let raw;
  try {
    raw = await readStdin();
  } catch (err) {
    const output = baseOutput('qa', modelNameForOutput);
    output.error = `failed to read stdin: ${err && err.message ? err.message : String(err)}`;
    process.stdout.write(JSON.stringify(output) + '\n');
    process.exitCode = 1;
    return;
  }

  let input;
  try {
    input = JSON.parse(raw);
  } catch (err) {
    const output = baseOutput('qa', modelNameForOutput);
    output.error = `malformed stdin JSON: ${err && err.message ? err.message : String(err)}`;
    process.stdout.write(JSON.stringify(output) + '\n');
    process.exitCode = 1;
    return;
  }

  const mode = typeof input.mode === 'string' && input.mode.length > 0 ? input.mode : 'qa';
  const output = baseOutput(mode, modelNameForOutput);

  const handler = MODE_HANDLERS[mode];
  if (!handler) {
    output.error = `unknown mode "${mode}" -- expected one of: ${Object.keys(MODE_HANDLERS).join(', ')}`;
    process.stdout.write(JSON.stringify(output) + '\n');
    process.exitCode = 1;
    return;
  }

  try {
    const env = resolveEnv();
    output.model = env.modelName;

    console.error(`PROGRESS: mode=${mode} model=${env.modelName}`);
    const fields = await handler(input, env);
    Object.assign(output, fields);
    output.error = '';

    process.stdout.write(JSON.stringify(output) + '\n');
  } catch (err) {
    output.error = err && err.message ? err.message : String(err);
    output.reply = '';
    output.input_tokens = -1;
    output.output_tokens = -1;
    process.stdout.write(JSON.stringify(output) + '\n');
    // Handled API/network/mode errors still exit 0 -- the `error` field is the
    // signal, per the arena contract (errors are data, not crashes).
  }
}

main().catch((err) => {
  // Last-resort guard: this should be unreachable since main() catches internally,
  // but if something outside the try/catch throws (e.g. a synchronous error before
  // output exists), still emit a best-effort JSON object to stdout before a
  // non-zero exit.
  const fallback = baseOutput('qa', process.env.OPENAI_MODEL || '');
  fallback.error = `uncaught adapter error: ${err && err.message ? err.message : String(err)}`;
  process.stdout.write(JSON.stringify(fallback) + '\n');
  process.exitCode = 1;
});
