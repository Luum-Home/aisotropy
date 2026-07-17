// `memory` mode -- REAL persisted conversational memory, proven across TWO
// separate process invocations (not an in-process Map that dies with the process).
//
// Turns are appended to a real table in the running, networked rqlite service
// (localhost:4001). A later invocation with the same `session_id`, run as a
// completely fresh `node index.mjs` process, loads that history back out of
// rqlite and prepends it before calling the model -- so a fact stated in
// invocation #1 is recalled in invocation #2 purely via the persisted store.

import { generateText } from 'ai';
import { rqliteExecute, rqliteQuery } from '../rqlite.mjs';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const TABLE = 'ai_sdk_arena_memory';

const DEFAULT_SYSTEM =
  'You are a helpful assistant with real persisted memory of this conversation. ' +
  'Use facts stated earlier in the conversation (shown as prior turns below) when answering.';

async function ensureTable(rqliteUrl) {
  await rqliteExecute(rqliteUrl, [
    `CREATE TABLE IF NOT EXISTS ${TABLE} (
       id INTEGER PRIMARY KEY AUTOINCREMENT,
       session_id TEXT NOT NULL,
       role TEXT NOT NULL,
       content TEXT NOT NULL,
       created_at TEXT NOT NULL
     )`,
  ]);
}

async function loadHistory(rqliteUrl, sessionId) {
  const result = await rqliteQuery(rqliteUrl, [
    `SELECT role, content FROM ${TABLE} WHERE session_id = ? ORDER BY id ASC`,
    sessionId,
  ]);
  const columns = result.columns || [];
  const values = result.values || [];
  const roleIdx = columns.indexOf('role');
  const contentIdx = columns.indexOf('content');
  if (roleIdx === -1 || contentIdx === -1) {
    return [];
  }
  return values.map((row) => ({ role: row[roleIdx], content: row[contentIdx] }));
}

async function appendTurns(rqliteUrl, sessionId, turns) {
  if (turns.length === 0) {
    return;
  }
  const now = new Date().toISOString();
  const statements = turns.map((turn) => [
    `INSERT INTO ${TABLE} (session_id, role, content, created_at) VALUES (?,?,?,?)`,
    sessionId,
    turn.role,
    turn.content,
    now,
  ]);
  await rqliteExecute(rqliteUrl, statements);
}

export async function runMemory(input, env) {
  const sessionId = typeof input.session_id === 'string' && input.session_id.length > 0
    ? input.session_id
    : null;
  if (!sessionId) {
    throw new Error(
      'memory mode requires a non-empty "session_id" string (same id across invocations to test recall)'
    );
  }

  const newMessages = buildMessages(input.messages);
  if (newMessages.length === 0) {
    throw new Error('memory mode requires at least one message in "messages"');
  }

  await ensureTable(env.rqliteUrl);
  const history = await loadHistory(env.rqliteUrl, sessionId);

  const allMessages = [...history, ...newMessages];
  const system = typeof input.system === 'string' && input.system.length > 0
    ? input.system
    : DEFAULT_SYSTEM;

  const startedAt = process.hrtime.bigint();
  const result = await generateText({
    model: env.chatModel,
    system,
    messages: allMessages,
  });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const reply = typeof result.text === 'string' ? result.text : '';

  // Persist this turn (the new user message(s) + the assistant's reply) so the
  // NEXT process invocation, given the same session_id, can recall it.
  await appendTurns(env.rqliteUrl, sessionId, [
    ...newMessages,
    { role: 'assistant', content: reply },
  ]);

  return {
    reply,
    input_tokens,
    output_tokens,
    latency_ms,
  };
}
