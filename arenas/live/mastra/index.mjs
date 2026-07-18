#!/usr/bin/env node
// Live arena adapter: Mastra (TypeScript AI framework, Apache-2.0), run here as
// plain ESM JS.
//
// EXHAUSTIVE multi-mode adapter (see README.md for the full contract). Every mode
// exercises a different Mastra NATIVE feature against REAL infrastructure -- no
// mocks, no stubs:
//   qa            - a single-shot Mastra `Agent` turn (agent.generate)
//   rag           - real e5 embed + real Qdrant search + a Mastra Agent grounded
//                   strictly in the retrieved context
//   tools         - a Mastra Agent + a real executed `calculate` tool, multi-step
//                   (maxSteps > 1) so the tool's real result is folded into the
//                   final answer
//   memory        - Mastra's NATIVE Memory primitive backed by on-disk libsql,
//                   proving conversational recall across TWO separate processes
//   memory_scoped - HAND-ROLLED compound scope (tenant/user/product[/session])
//                   over real rqlite -- same-scope hit + cross-tenant miss (Mastra
//                   Memory has only a 2-level resource/thread scope natively)
//   isolation_leak- plants a secret under one scope, recalls under another, and
//                   proves (or disproves) a cross-tenant leak via a real Mastra turn
//   structured    - Mastra NATIVE structured output (agent.generate +
//                   structuredOutput:{schema}) validated against a zod schema
//   stream        - Mastra NATIVE streaming (agent.stream) with time-to-first-token
//   observability - Mastra's NATIVE AI-tracing (@mastra/observability TestExporter,
//                   in-memory) -- a structured span tree + native metrics from one
//                   real tool-loop turn, no external collector, no OTel bridge
//   crash_recovery- proves survival of a HARD SIGKILL mid-conversation: role "run"
//                   checkpoints turns to real rqlite one by one; role "resume" is a
//                   fresh process that reloads and recalls the pre-crash fact
//
// Contract:
//   stdin  -> ONE JSON object, always including "mode" (defaults to "qa" when
//             absent, so the original single-shot arena task suite still works
//             unchanged against this adapter).
//   stdout -> exactly ONE JSON object (the adapter result). ALL logging goes to
//             stderr, prefixed "PROGRESS:" by convention. The OPENAI_API_KEY is
//             NEVER printed or logged on any path.

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

/** Read the entire stdin stream and return it as a UTF-8 string. */
async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

async function main() {
  // Contract hardening: stdout MUST carry exactly ONE JSON object (the adapter
  // result, always emitted via process.stdout.write below). Some dependencies
  // (@mastra/libsql storage warnings, the @mastra/observability TestExporter's
  // flush summary) diagnose via console.log, which would otherwise corrupt
  // stdout. Route every console.log to stderr so only our single JSON line can
  // ever reach stdout; PROGRESS logging already uses console.error directly.
  console.log = (...args) => console.error(...args);

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
  const fallback = baseOutput('qa', process.env.OPENAI_MODEL || '');
  fallback.error = `uncaught adapter error: ${err && err.message ? err.message : String(err)}`;
  process.stdout.write(JSON.stringify(fallback) + '\n');
  process.exitCode = 1;
});
