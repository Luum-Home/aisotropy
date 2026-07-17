// Hand-rolled compound-scope memory store over the real, networked rqlite
// service (the same infra `lib/rqlite.mjs` / `lib/modes/memory.mjs` already
// use) -- backing the `memory_scoped` and `isolation_leak` modes.
//
// HONEST NOTE: the Vercel AI SDK (`ai` + `@ai-sdk/openai`) has NO native
// concept of multi-tenant memory isolation. There is no "scope" primitive
// anywhere in the SDK -- `generateText()` just takes `messages`. Every
// isolation guarantee in this file is achieved by manually including all
// FOUR scope columns (tenant_id, user_id, product_id, session_id) in every
// WHERE clause below. A developer who forgets even one of these four columns
// in a query (e.g. filters by session_id alone, or by tenant_id + product_id
// but forgets user_id) creates a REAL cross-tenant data leak -- nothing in
// the AI SDK, in this schema, or in rqlite itself stops that mistake. That
// is the point of `lib/modes/isolation-leak.mjs`: to make this failure mode
// observable rather than assumed away. Contrast: aisotropy enforces tenant
// scoping by construction via `context.Context` propagation from the Huma
// handler boundary (see `docs/architecture/multi-tenancy.md`) -- there is no
// query path in aisotropy that CAN omit the tenant filter, because tenant_id
// never travels as a caller-suppliable query parameter in the first place.

import { rqliteExecute, rqliteQuery } from './rqlite.mjs';

const TABLE = 'ai_sdk_arena_scoped_memory';

/** Idempotently create the compound-scope facts table. */
export async function ensureScopedTable(rqliteUrl) {
  await rqliteExecute(rqliteUrl, [
    `CREATE TABLE IF NOT EXISTS ${TABLE} (
       id INTEGER PRIMARY KEY AUTOINCREMENT,
       tenant_id TEXT NOT NULL,
       user_id TEXT NOT NULL,
       product_id TEXT NOT NULL,
       session_id TEXT NOT NULL DEFAULT '',
       fact TEXT NOT NULL,
       created_at TEXT NOT NULL
     )`,
  ]);
}

/**
 * Parse a compound scope out of an arbitrary input object, requiring
 * tenant_id/user_id/product_id to be non-empty strings. session_id is
 * optional and defaults to '' (an empty session segment means "scoped to
 * this tenant+user+product regardless of session").
 */
export function requireScope(obj, label) {
  if (!obj || typeof obj !== 'object') {
    throw new Error(`missing "${label}" scope object`);
  }
  const tenantId = typeof obj.tenant_id === 'string' ? obj.tenant_id : '';
  const userId = typeof obj.user_id === 'string' ? obj.user_id : '';
  const productId = typeof obj.product_id === 'string' ? obj.product_id : '';
  if (!tenantId || !userId || !productId) {
    throw new Error(
      `"${label}" requires non-empty tenant_id, user_id, and product_id strings`
    );
  }
  const sessionId = typeof obj.session_id === 'string' ? obj.session_id : '';
  return { tenantId, userId, productId, sessionId };
}

/** Write one fact into the store under an EXACT compound scope. */
export async function writeScopedFact(rqliteUrl, scope, fact) {
  const now = new Date().toISOString();
  await rqliteExecute(rqliteUrl, [
    [
      `INSERT INTO ${TABLE} (tenant_id, user_id, product_id, session_id, fact, created_at) VALUES (?,?,?,?,?,?)`,
      scope.tenantId,
      scope.userId,
      scope.productId,
      scope.sessionId,
      fact,
      now,
    ],
  ]);
}

/**
 * Recall every fact stored under an EXACT compound scope match -- all four
 * columns are always included in the WHERE clause. This function's
 * implementation IS the isolation boundary: there is no framework-level
 * enforcement behind it, only this explicit AND chain.
 */
export async function recallScopedFacts(rqliteUrl, scope) {
  const result = await rqliteQuery(rqliteUrl, [
    `SELECT fact FROM ${TABLE}
       WHERE tenant_id = ? AND user_id = ? AND product_id = ? AND session_id = ?
       ORDER BY id ASC`,
    scope.tenantId,
    scope.userId,
    scope.productId,
    scope.sessionId,
  ]);
  const columns = result.columns || [];
  const values = result.values || [];
  const factIdx = columns.indexOf('fact');
  if (factIdx === -1) {
    return [];
  }
  return values.map((row) => row[factIdx]);
}
