// Offline, no-network substitute for the real rqlite service, used ONLY when
// ARENA_STUB=1 (see lib/env.mjs). Persists to a small JSON file under
// os.tmpdir() -- NOT a pure in-process Map -- specifically so cross-PROCESS
// semantics still hold under the stub: `crash_recovery`'s role="run"/"resume"
// and `memory_scoped`/`isolation_leak` (via lib/scoped-store.mjs) are each
// exercised across SEPARATE `node index.mjs` invocations, and a store that
// dies with the process would silently break exactly the behavior those modes
// exist to prove. A plain fs-JSON file keeps that property while adding zero
// network dependency.
//
// HONEST LIMITATION: this is NOT a SQL engine. It supports exactly the
// statement shapes this adapter's lib/rqlite.mjs callers use today (see
// lib/scoped-store.mjs and lib/modes/crash-recovery.mjs):
//   - CREATE TABLE IF NOT EXISTS <table> (...)
//   - INSERT INTO <table> (col1, col2, ...) VALUES (?, ?, ...)
//   - SELECT <col[, col...]> FROM <table> [WHERE col=? AND col=? ...] [ORDER BY ...]
// Any other statement shape throws (loudly, in the adapter's `error` field)
// rather than silently returning wrong results. No file locking: fine for
// this adapter's single-process-at-a-time usage pattern (test-crash.sh runs
// process A to completion, or kills it, before process B starts).

import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const STORE_PATH =
  process.env.ARENA_STUB_RQLITE_PATH || path.join(tmpdir(), 'arena_mastra_stub_rqlite.json');

const CREATE_TABLE_RE = /CREATE TABLE IF NOT EXISTS\s+(\w+)/i;
const INSERT_RE = /INSERT INTO\s+(\w+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)/i;
const SELECT_RE = /SELECT\s+(.+?)\s+FROM\s+(\w+)(?:\s+WHERE\s+(.+?))?(?:\s+ORDER BY\s+.+)?\s*$/i;

function loadDb() {
  if (!existsSync(STORE_PATH)) {
    return { tables: {} };
  }
  try {
    const parsed = JSON.parse(readFileSync(STORE_PATH, 'utf8'));
    return parsed && parsed.tables ? parsed : { tables: {} };
  } catch {
    // Corrupt or partially-written file (e.g. a prior stub run crashed mid
    // write) -- start fresh rather than propagating a parse error into every
    // mode. This mirrors "stub" honesty: best-effort offline substitute, not
    // a durability guarantee.
    return { tables: {} };
  }
}

function saveDb(db) {
  writeFileSync(STORE_PATH, JSON.stringify(db), 'utf8');
}

function ensureTable(db, table) {
  if (!db.tables[table]) {
    db.tables[table] = { nextId: 1, rows: [] };
  }
  return db.tables[table];
}

/** Stub equivalent of `rqliteExecute` -- runs CREATE TABLE / INSERT statements. */
export function stubExecute(statements) {
  const db = loadDb();
  for (const stmt of statements) {
    const sql = Array.isArray(stmt) ? stmt[0] : stmt;
    const params = Array.isArray(stmt) ? stmt.slice(1) : [];

    const createMatch = sql.match(CREATE_TABLE_RE);
    if (createMatch) {
      ensureTable(db, createMatch[1]);
      continue;
    }

    const insertMatch = sql.match(INSERT_RE);
    if (insertMatch) {
      const [, table, colsRaw] = insertMatch;
      const cols = colsRaw.split(',').map((c) => c.trim());
      const t = ensureTable(db, table);
      const row = { id: t.nextId };
      cols.forEach((col, i) => {
        row[col] = params[i];
      });
      t.nextId += 1;
      t.rows.push(row);
      continue;
    }

    throw new Error(
      `arena stub store (ARENA_STUB=1): unsupported statement shape: ${sql.slice(0, 160)}`
    );
  }
  saveDb(db);
  return { results: statements.map(() => ({})) };
}

/** Stub equivalent of `rqliteQuery` -- runs a single-table filtered SELECT. */
export function stubQuery(statement) {
  const sql = Array.isArray(statement) ? statement[0] : statement;
  const params = Array.isArray(statement) ? statement.slice(1) : [];

  const selectMatch = sql.match(SELECT_RE);
  if (!selectMatch) {
    throw new Error(`arena stub store (ARENA_STUB=1): unsupported query shape: ${sql.slice(0, 160)}`);
  }
  const [, colsRaw, table, whereRaw] = selectMatch;
  const cols = colsRaw.split(',').map((c) => c.trim());

  const db = loadDb();
  const t = db.tables[table] || { rows: [] };
  let rows = t.rows;

  if (whereRaw) {
    const whereCols = [...whereRaw.matchAll(/(\w+)\s*=\s*\?/g)].map((m) => m[1]);
    rows = rows.filter((row) => whereCols.every((col, i) => row[col] === params[i]));
  }
  rows = [...rows].sort((a, b) => a.id - b.id);

  const values = rows.map((row) => cols.map((c) => row[c]));
  return { columns: cols, types: cols.map(() => 'text'), values };
}
