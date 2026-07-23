// `isolation_forgetful` mode -- the "forgetful integrator" tenancy probe
// (docs/architecture/arena-tenancy-levels.md §"forgetful integrator";
// docs/architecture/arena-idiomaticity-and-dx.md fairness constraint).
//
// The runner has ALREADY written a fact under scope A through Mastra's OWN memory
// API (the PERSIST leg arrives as an ordinary mode:"memory" turn carrying
// user_id=A, so it runs through the resource-scoped path in lib/modes/memory.mjs,
// writing under resourceId=A). This handler is the RECALL leg: it reads back with
// the user scope DELIBERATELY OMITTED (the wire sends user_id ""), the exact
// mistake a real integrator makes by accident.
//
// FAIRNESS (the load-bearing rule): the omission must happen at MASTRA'S OWN API
// BOUNDARY, never patched over by adapter glue. So this handler forwards the
// resourceId RAW — when user_id is blank, `resource` is left UNDEFINED on the
// agent.generate({ memory: {...} }) call. Whatever Mastra does with an omitted
// resourceId is MASTRA'S verdict, scored three ways by the runner
// (scoring.ScoreForgetful):
//   - THROWS / refuses  -> enforcement (PASS): the scope cannot be silently
//     forgotten. Observed offline: Mastra throws "A resourceId and a threadId must
//     be provided when using Memory. Saw threadId <B> and resourceId undefined"
//     from @mastra/core (chunk-34TKMNFP.js) BEFORE any model call — the L2-style
//     boundary refusal (docs/architecture/arena-tenancy-levels.md places Mastra at
//     L1 as a library / L2 with the server). That error propagates verbatim to the
//     contract's `error` field via index.mjs's top-level catch — never reshaped
//     into another framework's wording.
//   - RETURNS scope-A data -> leak (FAIL): the omitted read surfaced the fact.
//   - RETURNS empty, no error -> silent_partition (FAIL): the omission quietly
//     created a DIFFERENT/empty scope, the invisible L1 failure mode.
//
// The handler does NOT catch Mastra's throw: swallowing it and returning a shaped
// message would be exactly the adapter-glue interference the fairness rule
// forbids. On the (not-observed) no-throw path it returns Mastra's reply so the
// runner can distinguish leak from silent_partition.

import { Agent } from '@mastra/core/agent';
import { buildResourceScopedMemory } from '../memory-store.mjs';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS =
  'You are a helpful assistant with real persisted memory. ' +
  'Answer using facts you remember about this user; if you remember nothing, say so.';

export async function runIsolationForgetful(input, env) {
  const sessionId =
    typeof input.session_id === 'string' && input.session_id.length > 0 ? input.session_id : null;
  if (!sessionId) {
    throw new Error(
      'isolation_forgetful mode requires a non-empty "session_id" string (the recall thread)'
    );
  }
  const newMessages = buildMessages(input.messages);
  if (newMessages.length === 0) {
    throw new Error('isolation_forgetful mode requires at least one message in "messages"');
  }

  // THE OMISSION: user_id (-> resourceId) is intentionally forwarded RAW. Blank ->
  // undefined -> Mastra's Memory API sees no resourceId. No default backfill.
  const hasUser = typeof input.user_id === 'string' && input.user_id.length > 0;
  const resourceId = hasUser ? input.user_id : undefined;

  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;

  const agent = new Agent({
    name: 'arena-isolation-forgetful',
    instructions,
    model: env.chatModel,
    memory: buildResourceScopedMemory(env),
  });

  // Build the memory option WITHOUT a `resource` key when the scope was omitted —
  // forwarding `resource: undefined` and omitting it are equivalent at Mastra's
  // boundary, but omitting the key makes the intent unambiguous in the wire call.
  const memOpt = { thread: sessionId };
  if (resourceId !== undefined) {
    memOpt.resource = resourceId;
  }

  const startedAt = process.hrtime.bigint();
  // If Mastra refuses (throws) this rejects and the error bubbles to index.mjs's
  // top-level catch -> contract `error` field, verbatim. That IS the pass signal.
  const result = await agent.generate(newMessages, { memory: memOpt });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const reply = typeof result.text === 'string' ? result.text : '';

  return {
    reply,
    input_tokens,
    output_tokens,
    latency_ms,
    // Honest signal for the no-throw path: the runner's ScoreForgetful reads
    // `reply` for the scoped fact (leak) vs emptiness (silent_partition).
    recalled: reply,
    notes:
      'isolation_forgetful: resourceId omitted at Mastra Memory boundary (user_id blank); ' +
      'Mastra did NOT refuse on this path — runner classifies leak vs silent_partition from reply',
  };
}
