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
// TWO configurations, selected by whether the wire carries a `user_id`:
//
//   - THREAD-SCOPED (no user_id) — the ORIGINAL `multi_turn_memory` dimension.
//     `semanticRecall:false`, `lastMessages:30`, thread-only. The invocations
//     share ONE session_id, so thread-scoped recall is exactly right; the
//     idiomaticity doc (docs/architecture/arena-idiomaticity-and-dx.md) records
//     this `semanticRecall:false` as a JUSTIFIED deviation for THIS dimension.
//
//   - RESOURCE-SCOPED (user_id present) — the memory-EXTENSION dimensions
//     (memory_cross_session / memory_semantic_recall / memory_consolidation, and
//     the persist leg of isolation_forgetful). These VARY the session_id
//     (threadId) while HOLDING the user_id (resourceId), so thread-scoped recall
//     would find nothing on the fresh recall session by construction. Mastra's
//     official cross-thread primitive is `scope:'resource'` on both working memory
//     and semanticRecall (lib/memory-store.mjs) — the idiomaticity doc's
//     SHOULD-FIX turning semanticRecall ON for exactly these dimensions.

import { Agent } from '@mastra/core/agent';
import { Memory } from '@mastra/memory';
import { LibSQLStore } from '@mastra/libsql';
import { buildResourceScopedMemory } from '../memory-store.mjs';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS =
  'You are a helpful assistant with real persisted memory of this conversation. ' +
  'Use facts stated earlier in the conversation (shown as prior turns) when answering.';

// Thread-scoped Memory for the ORIGINAL memory dimension (no user_id): durable
// conversational recall keyed by (resourceId, threadId) where the threadId (the
// shared session_id) carries the recall — semanticRecall intentionally off.
function buildThreadMemory(libsqlUrl) {
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

  // The wire's user_id IS Mastra's resourceId (docs/architecture/arena-tenancy-
  // levels.md). Its PRESENCE selects the configuration: present -> the memory-
  // extension dimensions vary session_id/hold user_id, so recall must be
  // resource-scoped across threads; absent -> the original thread-scoped
  // dimension with a stable arena-default resource.
  const hasUser = typeof input.user_id === 'string' && input.user_id.length > 0;
  const resourceId = hasUser ? input.user_id : 'arena-user';

  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;

  const memory = hasUser ? buildResourceScopedMemory(env) : buildThreadMemory(env.libsqlUrl);

  const agent = new Agent({
    name: 'arena-memory',
    instructions,
    model: env.chatModel,
    memory,
  });

  const startedAt = process.hrtime.bigint();
  // Mastra auto-persists these new messages + the assistant reply to the libsql
  // store under (resourceId, threadId), and auto-loads prior turns before the
  // model call. In the resource-scoped configuration it ALSO retrieves relevant
  // messages from OTHER threads of the same resourceId (semanticRecall +
  // working-memory profile, scope:'resource') -- the cross-session mechanic.
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
