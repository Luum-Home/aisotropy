// `memory` mode -- Mastra's NATIVE `Memory` primitive, proven across TWO separate
// process invocations (not an in-process Map that dies with the process).
//
// This is the Mastra-native path (contrast the hand-rolled rqlite store used by
// memory_scoped / isolation_leak / crash_recovery, where Mastra has no native
// primitive). A `Memory` backed by an on-disk libsql/SQLite file
// (@mastra/libsql `LibSQLStore`) auto-persists each turn's user + assistant
// messages, keyed by (resourceId, threadId). A later invocation with the same
// session_id, run as a completely fresh `node index.mjs` process, reopens that
// same libsql file and Mastra loads the prior turns back before calling the model
// -- so a fact stated in invocation #1 is recalled in invocation #2 purely via the
// persisted store.
//
// semanticRecall is disabled (it would require an embedder and a vector index);
// this mode proves durable conversational recall via Mastra's message store, the
// dimension the arena `memory` task grades.

import { Agent } from '@mastra/core/agent';
import { Memory } from '@mastra/memory';
import { LibSQLStore } from '@mastra/libsql';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS =
  'You are a helpful assistant with real persisted memory of this conversation. ' +
  'Use facts stated earlier in the conversation (shown as prior turns) when answering.';

function buildMemory(libsqlUrl) {
  const storage = new LibSQLStore({ id: 'arena-mastra-memory', url: libsqlUrl });
  return new Memory({
    storage,
    options: {
      semanticRecall: false,
      lastMessages: 30,
      workingMemory: { enabled: false },
    },
    generateTitle: false,
  });
}

export async function runMemory(input, env) {
  const sessionId =
    typeof input.session_id === 'string' && input.session_id.length > 0 ? input.session_id : null;
  if (!sessionId) {
    throw new Error(
      'memory mode requires a non-empty "session_id" string (same id across invocations to test recall)'
    );
  }
  const newMessages = buildMessages(input.messages);
  if (newMessages.length === 0) {
    throw new Error('memory mode requires at least one message in "messages"');
  }

  // resourceId groups threads by owner; default to the caller's user_id when
  // provided, else a stable arena default. threadId IS the session_id.
  const resourceId =
    typeof input.user_id === 'string' && input.user_id.length > 0 ? input.user_id : 'arena-user';

  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;

  const agent = new Agent({
    name: 'arena-memory',
    instructions,
    model: env.chatModel,
    memory: buildMemory(env.libsqlUrl),
  });

  const startedAt = process.hrtime.bigint();
  // Mastra auto-persists these new messages + the assistant reply to the libsql
  // store under (resourceId, threadId), and auto-loads prior turns for the same
  // thread before the model call -- no manual history append needed.
  const result = await agent.generate(newMessages, {
    memory: { resource: resourceId, thread: sessionId },
  });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
  };
}
