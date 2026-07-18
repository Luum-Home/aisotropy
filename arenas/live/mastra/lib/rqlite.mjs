// Minimal rqlite HTTP client (https://rqlite.io/docs/api/api/) built on Node's
// native `fetch` -- no extra dependency needed. Used by the `memory` mode to
// persist conversation turns to the REAL, networked rqlite service so a fact
// stated in one process invocation can be recalled by a completely separate,
// later process invocation (real persistence, not an in-memory Map).

/**
 * Run one or more write statements. Each statement is either a bare SQL string
 * (no params) or an array `[sql, ...params]` for a parameterized statement, per
 * rqlite's request body shape. Throws on any transport or per-statement error.
 */
export async function rqliteExecute(rqliteUrl, statements) {
  const res = await fetch(`${rqliteUrl}/db/execute`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
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
  const res = await fetch(`${rqliteUrl}/db/query?level=strong`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
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
