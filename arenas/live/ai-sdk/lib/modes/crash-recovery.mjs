// `crash_recovery` mode -- proves whether the AI SDK app survives a HARD CRASH
// (SIGKILL) mid-conversation and resumes, as opposed to `memory` mode
// (lib/modes/memory.mjs), which only proves persistence across a CLEAN process
// exit -- that adapter always runs a single stdin/stdout turn to completion
// and exits 0 before the next invocation ever starts. Nothing about that
// proves survival of a kill -9 while a turn is in flight.
//
// Two roles, both driven through this ONE mode so the adapter's stdin/stdout
// contract stays uniform (see index.mjs) -- test-crash.sh runs each role as a
// SEPARATE `node index.mjs` process invocation:
//
//   role: "run"    -- "process A". Runs a short (2-3 turn) conversation
//                      IN-PROCESS, turn by turn: call agnes, then persist that
//                      turn's user+assistant messages to rqlite in ONE atomic
//                      `/db/execute` call (rqlite runs a multi-statement
//                      execute request as a single implicit transaction) --
//                      BEFORE starting the next turn. test-crash.sh SIGKILLs
//                      this process on purpose, deliberately AFTER turn 1's
//                      checkpoint but WHILE turn 2's own generateText() call
//                      is still in flight -- so turn 2 is never committed.
//
//   role: "resume" -- "process B". A COMPLETELY FRESH `node index.mjs`
//                      process (new PID, new heap -- not a retry of process
//                      A) that loads whatever got durably committed to rqlite
//                      for the same session_id, reports whether that
//                      checkpoint survived intact (`resumed`, `state_lost`),
//                      and continues the conversation with a REAL agnes call
//                      that must recall the pre-crash fact purely from the
//                      recovered rqlite history (`precrash_fact_recalled`).
//
// `state_lost` is deliberately about DURABLE state only -- checked directly
// against what process B loaded from rqlite, not about model behavior. The
// checkpoint-after-each-turn design means a crash can only ever discard
// in-flight, never-committed work (turn 2 here); it can never corrupt or
// erase an already-committed turn (turn 1). So the expected, honest finding
// for THIS scenario is state_lost=false (the checkpointed fact survived) even
// though the in-flight turn 2 itself is gone -- see README.md's HONEST note.

import { generateText } from 'ai';
import { rqliteExecute, rqliteQuery } from '../rqlite.mjs';
import { extractTokenCounts, msSince } from '../output.mjs';

const TABLE = 'ai_sdk_arena_crash_recovery';

const DEFAULT_SYSTEM =
  'You are a helpful assistant. Use facts stated earlier in this conversation ' +
  '(shown as prior turns below) when answering later questions.';

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

// Persists exactly one turn's user+assistant messages in a SINGLE rqlite
// `/db/execute` call. Both INSERTs land in one implicit transaction, so this
// checkpoint is all-or-nothing: a crash before this call returns leaves ZERO
// rows for the turn behind (never a half-written one).
async function commitTurn(rqliteUrl, sessionId, userContent, assistantContent) {
  const now = new Date().toISOString();
  await rqliteExecute(rqliteUrl, [
    [
      `INSERT INTO ${TABLE} (session_id, role, content, created_at) VALUES (?,?,?,?)`,
      sessionId,
      'user',
      userContent,
      now,
    ],
    [
      `INSERT INTO ${TABLE} (session_id, role, content, created_at) VALUES (?,?,?,?)`,
      sessionId,
      'assistant',
      assistantContent,
      now,
    ],
  ]);
}

function requireSessionId(input) {
  const sessionId =
    typeof input.session_id === 'string' && input.session_id.length > 0 ? input.session_id : null;
  if (!sessionId) {
    throw new Error('crash_recovery mode requires a non-empty "session_id" string');
  }
  return sessionId;
}

function historyToText(history) {
  return history.map((h) => `${h.role}: ${h.content}`).join('\n');
}

async function callAgnes(env, system, history, userContent, turnLabel) {
  console.error(`PROGRESS: crash_recovery ${turnLabel} calling agnes`);
  const messages = [...history, { role: 'user', content: userContent }];
  const result = await generateText({ model: env.chatModel, system, messages });
  const reply = typeof result.text === 'string' ? result.text : '';
  console.error(`PROGRESS: crash_recovery ${turnLabel} agnes replied`);
  return { reply, usage: result.usage };
}

// role: "run" (process A) -- runs a short scripted conversation turn by turn,
// checkpointing to rqlite after EACH turn completes. Intended to be SIGKILLed
// by test-crash.sh partway through a later turn; if it ever runs to
// completion uninterrupted, it still returns a normal adapter result.
async function runProcessA(input, env) {
  const sessionId = requireSessionId(input);
  const turns = Array.isArray(input.turns)
    ? input.turns.filter((t) => typeof t === 'string' && t.length > 0)
    : [];
  if (turns.length < 2 || turns.length > 3) {
    throw new Error('crash_recovery role="run" requires 2 or 3 "turns" (non-empty strings)');
  }
  const system =
    typeof input.system === 'string' && input.system.length > 0 ? input.system : DEFAULT_SYSTEM;

  await ensureTable(env.rqliteUrl);
  let history = await loadHistory(env.rqliteUrl, sessionId);

  const startedAt = process.hrtime.bigint();
  let lastReply = '';
  let lastUsage = null;
  for (let i = 0; i < turns.length; i += 1) {
    const label = `turn ${i + 1}/${turns.length}`;
    const { reply, usage } = await callAgnes(env, system, history, turns[i], label);
    // Checkpoint BEFORE moving on to the next turn -- this is the exact
    // instant test-crash.sh is not allowed to catch us before (turn 1) but IS
    // aiming to SIGKILL us during (turn 2's callAgnes() above, before this
    // commitTurn() line ever runs).
    await commitTurn(env.rqliteUrl, sessionId, turns[i], reply);
    history = [...history, { role: 'user', content: turns[i] }, { role: 'assistant', content: reply }];
    lastReply = reply;
    lastUsage = usage;
    console.error(`PROGRESS: crash_recovery ${label} committed (session history now ${history.length} row(s))`);
  }
  const latency_ms = msSince(startedAt);
  const { input_tokens, output_tokens } = extractTokenCounts(lastUsage);

  return {
    reply: lastReply,
    input_tokens,
    output_tokens,
    latency_ms,
    recalled: historyToText(history),
  };
}

// role: "resume" (process B) -- a fresh process, loads whatever rqlite has for
// session_id and continues the conversation with one real agnes call.
async function runProcessB(input, env) {
  const sessionId = requireSessionId(input);
  const continuation =
    typeof input.continuation === 'string' && input.continuation.length > 0
      ? input.continuation
      : null;
  if (!continuation) {
    throw new Error('crash_recovery role="resume" requires a non-empty "continuation" string');
  }
  const expectedFact = typeof input.expected_fact === 'string' ? input.expected_fact : '';
  const system =
    typeof input.system === 'string' && input.system.length > 0 ? input.system : DEFAULT_SYSTEM;

  await ensureTable(env.rqliteUrl);
  const history = await loadHistory(env.rqliteUrl, sessionId);
  const resumed = history.length > 0;

  // Durable-state check, computed directly against what THIS FRESH PROCESS
  // loaded from rqlite -- independent of what the model does next. True only
  // if nothing survived at all, or the specific pre-crash checkpoint (the
  // fact) is missing from what did survive.
  const state_lost =
    !resumed ||
    (expectedFact.length > 0 &&
      !history.some((h) => typeof h.content === 'string' && h.content.includes(expectedFact)));

  console.error(
    `PROGRESS: crash_recovery resume loaded ${history.length} persisted row(s) for session, resumed=${resumed}, state_lost=${state_lost}`
  );

  const startedAt = process.hrtime.bigint();
  const { reply, usage } = await callAgnes(env, system, history, continuation, 'resume-turn');
  const latency_ms = msSince(startedAt);

  // Keep the resumed conversation itself durable too (consistency, not part
  // of the acceptance criteria).
  await commitTurn(env.rqliteUrl, sessionId, continuation, reply);

  const { input_tokens, output_tokens } = extractTokenCounts(usage);
  const precrash_fact_recalled =
    expectedFact.length > 0 ? reply.toLowerCase().includes(expectedFact.toLowerCase()) : null;

  return {
    reply,
    input_tokens,
    output_tokens,
    latency_ms,
    recalled: historyToText(history),
    resumed,
    state_lost,
    precrash_fact_recalled,
  };
}

export async function runCrashRecovery(input, env) {
  const role = typeof input.role === 'string' && input.role.length > 0 ? input.role : null;
  if (role === 'run') {
    return runProcessA(input, env);
  }
  if (role === 'resume') {
    return runProcessB(input, env);
  }
  throw new Error('crash_recovery mode requires "role": "run" (process A) or "resume" (process B)');
}
