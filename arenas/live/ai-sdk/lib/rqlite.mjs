// Minimal rqlite HTTP client (https://rqlite.io/docs/api/api/) built on Node's
// native `fetch` -- no extra dependency needed. Used by the `memory` mode to
// persist conversation turns to the REAL, networked rqlite service so a fact
// stated in one process invocation can be recalled by a completely separate,
// later process invocation (real persistence, not an in-memory Map).
//
// STUB ROUTING (ARENA_STUB=1): when `rqliteUrl` is the sentinel
// `stub:local` (set by `lib/env.mjs`, see `lib/stub.mjs`), every call below
// is routed to `stubExecute`/`stubQuery` instead of `fetch()` -- a tiny local
// table engine, persisted to a JSON file per table under the OS temp dir so
// cross-PROCESS callers (crash_recovery's role:"run"/"resume" pair) still
// see each other's writes, with ZERO network egress. This is the ONLY branch
// point: every mode file (`memory.mjs`, `memory-scoped.mjs` via
// `scoped-store.mjs`, `crash-recovery.mjs`) calls `rqliteExecute`/
// `rqliteQuery` exactly as before and never needs to know which backend is live.

import { stubExecute, stubQuery } from './rqlite-stub.mjs';

const STUB_URL = 'stub:local';

/**
 * Split credentials out of an rqlite URL into a clean URL + auth headers.
 *
 * WHY: the arena's rqlite runs with `-auth`, so requests must authenticate. The
 * obvious mechanism — userinfo in the URL, `http://user:pass@host:4001` — is a
 * trap across runtimes: Node's `fetch` REJECTS such a URL outright ("Request
 * cannot be constructed from a URL that includes credentials"), Python's
 * urllib misreads it, and Java silently drops the credentials and gets a 401.
 * Three different failures for one URL. So every client here strips the
 * userinfo and sends `Authorization: Basic` instead, which all of them honour.
 * A URL without credentials passes through untouched, so a keyless local rqlite
 * still works.
 */
function rqliteTarget(rqliteUrl) {
  const headers = { 'content-type': 'application/json' };
  try {
    const u = new URL(rqliteUrl);
    if (u.username || u.password) {
      const basic = Buffer.from(`${decodeURIComponent(u.username)}:${decodeURIComponent(u.password)}`).toString('base64');
      headers.authorization = `Basic ${basic}`;
      u.username = '';
      u.password = '';
      return { base: u.toString().replace(/\/$/, ''), headers };
    }
  } catch {
    // Not a parseable URL (e.g. the stub sentinel) — leave it alone.
  }
  return { base: rqliteUrl, headers };
}

/**
 * Run one or more write statements. Each statement is either a bare SQL string
 * (no params) or an array `[sql, ...params]` for a parameterized statement, per
 * rqlite's request body shape. Throws on any transport or per-statement error.
 */
export async function rqliteExecute(rqliteUrl, statements) {
  if (rqliteUrl === STUB_URL) {
    return stubExecute(statements);
  }
  const target = rqliteTarget(rqliteUrl);
  const res = await fetch(`${target.base}/db/execute`, {
    method: 'POST',
    headers: target.headers,
    body: JSON.stringify(statements),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(`rqlite execute failed: HTTP ${res.status} ${JSON.stringify(data)}`);
  }
  for (const result of data.results || []) {
    if (result && result.error) {
      throw new Error(`rqlite execute error: ${result.error}`);
    }
  }
  return data;
}

/**
 * Run exactly one read statement (`[sql, ...params]` or a bare SQL string) and
 * return its single result object `{ columns, types, values }` (or `{}` if the
 * query matched no rows). Uses `level=strong` so a read immediately after a write
 * on the same single-node rqlite always observes that write.
 */
export async function rqliteQuery(rqliteUrl, statement) {
  if (rqliteUrl === STUB_URL) {
    return stubQuery(statement);
  }
  const target = rqliteTarget(rqliteUrl);
  const res = await fetch(`${target.base}/db/query?level=strong`, {
    method: 'POST',
    headers: target.headers,
    body: JSON.stringify([statement]),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(`rqlite query failed: HTTP ${res.status} ${JSON.stringify(data)}`);
  }
  const result = (data.results || [])[0];
  if (result && result.error) {
    throw new Error(`rqlite query error: ${result.error}`);
  }
  return result || {};
}
