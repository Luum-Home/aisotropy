// `memory_scoped` mode -- REAL persisted memory keyed by a HAND-ROLLED
// compound scope (tenant_id, user_id, product_id[, session_id]) over the
// same real, networked rqlite service `memory` mode uses.
//
// WHY: the base `memory` mode (lib/modes/memory.mjs) keys purely by
// session_id -- it proves persistence across process invocations, but it
// does NOT test scoped/multi-tenant memory or isolation, which is
// aisotropy's differentiator (see docs/architecture/multi-tenancy.md). This
// mode proves the compound-scope property instead: a fact written under one
// (tenant_id, user_id, product_id[, session_id]) scope is recalled under
// that EXACT scope, and is invisible under a different scope. See
// `lib/scoped-store.mjs` for the honest caveat: the AI SDK has no native
// primitive for any of this -- it is entirely hand-rolled here.
//
// Single invocation proves all three legs in one real agnes turn:
//   1. WRITE  `fact` under the primary (tenant_id,user_id,product_id,session_id)
//      scope, if `fact` is provided (omit it to test recall-only, e.g. to
//      prove persistence across a SEPARATE process invocation).
//   2. RECALL under that SAME primary scope -> same_scope_hit.
//   3. RECALL under `cross_scope` (a caller-supplied different-tenant probe,
//      typically a different tenant_id) -> cross_tenant_miss.
// The same-scope recalled facts are then injected as CONTEXT for a real
// generateText() call answering `query`, exactly like `rag` mode grounds its
// answer in retrieved passages -- so the scoped recall is proven live, not
// just asserted from a raw SQL result.

import { generateText } from 'ai';
import {
  ensureScopedTable,
  requireScope,
  writeScopedFact,
  recallScopedFacts,
} from '../scoped-store.mjs';
import { extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_QUERY =
  'What do you remember about me for this product, based only on the context above?';

const SYSTEM_PREFIX =
  'You are a scoped memory assistant. Below is CONTEXT: zero or more facts recalled ' +
  'from a per-tenant memory store for the CURRENT scope only. Answer the question using ' +
  'ONLY these facts. If no facts are listed, say you have no memory of this scope -- do not guess.';

export async function runMemoryScoped(input, env) {
  const scope = requireScope(
    input,
    'primary scope (top-level tenant_id/user_id/product_id[/session_id])'
  );

  const fact = typeof input.fact === 'string' && input.fact.length > 0 ? input.fact : null;
  const query =
    typeof input.query === 'string' && input.query.length > 0 ? input.query : DEFAULT_QUERY;

  await ensureScopedTable(env.rqliteUrl);

  if (fact) {
    await writeScopedFact(env.rqliteUrl, scope, fact);
  }

  const sameScopeFacts = await recallScopedFacts(env.rqliteUrl, scope);
  const same_scope_hit = fact ? sameScopeFacts.includes(fact) : sameScopeFacts.length > 0;

  let cross_tenant_miss = true;
  let crossScopeFacts = [];
  if (input.cross_scope) {
    const crossScope = requireScope(
      input.cross_scope,
      'cross_scope (the different-tenant probe used to prove isolation)'
    );
    crossScopeFacts = await recallScopedFacts(env.rqliteUrl, crossScope);
    cross_tenant_miss = fact ? !crossScopeFacts.includes(fact) : crossScopeFacts.length === 0;
  }

  const contextBlock =
    sameScopeFacts.length > 0
      ? sameScopeFacts.map((f, i) => `${i + 1}. ${f}`).join('\n')
      : '(no facts recalled for this scope)';
  const system = `${SYSTEM_PREFIX}\n\nCONTEXT:\n${contextBlock}`;

  const startedAt = process.hrtime.bigint();
  const result = await generateText({
    model: env.chatModel,
    system,
    messages: [{ role: 'user', content: query }],
  });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    recalled: sameScopeFacts.join('\n'),
    scoped_recall: {
      same_scope_hit,
      cross_tenant_miss,
      same_scope_facts: sameScopeFacts,
      cross_scope_facts: crossScopeFacts,
    },
  };
}
