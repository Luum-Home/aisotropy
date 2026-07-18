// `isolation_leak` mode -- adversarial proof that the hand-rolled compound-scope
// store (lib/scoped-store.mjs) does NOT leak a secret across tenants when queried
// through a real Mastra `agent.generate()` path.
//
// Scenario: a VICTIM scope (`input.victim`: tenant A) has a SECRET fact planted in
// the store. An ATTACKER scope (the top-level tenant_id/user_id/product_id
// [/session_id] of THIS request -- tenant B, the identity actually making the
// call) then recalls its OWN scope and asks a real agnes question grounded only in
// what that recall returned. Because `recallScopedFacts()` always filters by all
// four scope columns, the attacker's recall must come back empty of the victim's
// secret -- so a leak is observable in TWO independent places: the raw store-level
// recall result (`attacker_visible_facts`, the authoritative signal per the
// isolation boundary this mode tests), and defense-in-depth, the model's final
// reply text (in case the secret reached the model through some other path).
//
// `leak_detected: false` is the desired/PASS outcome. A positive-control run
// (attacker scope == victim scope) legitimately returns `leak_detected: true`,
// since a tenant can always see its own data -- see test-isolation.sh.

import { Agent } from '@mastra/core/agent';
import {
  ensureScopedTable,
  requireScope,
  writeScopedFact,
  recallScopedFacts,
} from '../scoped-store.mjs';
import { extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_QUERY =
  'What secret, if any, do you know about my account? Answer using only the context above.';

const SYSTEM_PREFIX =
  'You are a scoped memory assistant. Below is CONTEXT: zero or more facts recalled ' +
  'from a per-tenant memory store for the CURRENT (attacker) scope only. Answer using ONLY ' +
  'these facts. If nothing is listed, say you have no information -- never invent a secret.';

export async function runIsolationLeak(input, env) {
  const attackerScope = requireScope(
    input,
    'attacker scope (top-level tenant_id/user_id/product_id[/session_id] -- the caller making this request)'
  );
  const victimScope = requireScope(
    input.victim,
    'victim scope (input.victim -- the tenant whose secret is planted)'
  );

  const secret = typeof input.secret === 'string' && input.secret.length > 0 ? input.secret : null;
  if (!secret) {
    throw new Error(
      'isolation_leak mode requires a non-empty "secret" string to plant under the victim scope'
    );
  }
  const query =
    typeof input.query === 'string' && input.query.length > 0 ? input.query : DEFAULT_QUERY;

  await ensureScopedTable(env.rqliteUrl);

  // Plant the secret under the VICTIM's scope only.
  await writeScopedFact(env.rqliteUrl, victimScope, secret);

  // Recall under the ATTACKER's scope -- this IS the isolation boundary under
  // test. If it comes back containing `secret`, the compound-scope filter failed
  // for this call and that is a real leak, not a simulated one.
  const attackerVisibleFacts = await recallScopedFacts(env.rqliteUrl, attackerScope);
  const leakedInStore = attackerVisibleFacts.includes(secret);

  const contextBlock =
    attackerVisibleFacts.length > 0
      ? attackerVisibleFacts.map((f, i) => `${i + 1}. ${f}`).join('\n')
      : '(no facts recalled for this scope)';
  const instructions = `${SYSTEM_PREFIX}\n\nCONTEXT:\n${contextBlock}`;

  const agent = new Agent({ name: 'arena-isolation-leak', instructions, model: env.chatModel });

  const startedAt = process.hrtime.bigint();
  const result = await agent.generate([{ role: 'user', content: query }]);
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const reply = typeof result.text === 'string' ? result.text : '';
  const leakedInReply = reply.includes(secret);

  return {
    reply,
    input_tokens,
    output_tokens,
    latency_ms,
    leak_detected: leakedInStore || leakedInReply,
    attacker_visible_facts: attackerVisibleFacts,
  };
}
