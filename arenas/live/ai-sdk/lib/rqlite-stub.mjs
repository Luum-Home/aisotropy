// Tiny local table engine backing `lib/rqlite.mjs`'s `stub:local` route
// (ARENA_STUB=1). NOT a general SQL engine -- it only understands the exact
// three statement shapes this adapter's rqlite consumers emit:
//   1. `CREATE TABLE IF NOT EXISTS <table> (...)`
//   2. `INSERT INTO <table> (col, col, ...) VALUES (?,?,...)` + positional params
//   3. `SELECT <cols> FROM <table> WHERE <col> = ? [AND <col> = ? ...] ORDER BY id ASC`
// (see `lib/modes/memory.mjs`, `lib/modes/crash-recovery.mjs`, and
// `lib/scoped-store.mjs`, all unmodified -- they call `rqliteExecute`/
// `rqliteQuery` exactly as they do against real rqlite).
//
// Persistence: each table is one JSON file under the OS temp dir, so a
// SEPARATE process invocation (crash_recovery's role:"resume", a completely
// fresh `node index.mjs`) still observes what an earlier invocation
// committed -- proving cross-process persistence semantics stay meaningful
// under the stub, without ever opening a network socket.

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

const STUB_DIR = path.join(os.tmpdir(), 'aisotropy-arena-ai-sdk-stub');

function tablePath(table) {
  return path.join(STUB_DIR, `${table}.json`);
}

function loadTable(table) {
  try {
    const raw = fs.readFileSync(tablePath(table), 'utf8');
    const parsed = JSON.parse(raw);
    return { nextId: parsed.nextId || 1, rows: Array.isArray(parsed.rows) ? parsed.rows : [] };
  } catch {
    return { nextId: 1, rows: [] };
  }
}

function saveTable(table, data) {
  fs.mkdirSync(STUB_DIR, { recursive: true });
  fs.writeFileSync(tablePath(table), JSON.stringify(data), 'utf8');
}

function parseCreateTable(sql) {
  const m = sql.match(/CREATE TABLE IF NOT EXISTS\s+(\w+)/i);
  return m ? m[1] : null;
}

function parseInsert(sql) {
  const m = sql.match(/INSERT INTO\s+(\w+)\s*\(([^)]+)\)\s*VALUES/i);
  if (!m) {
    return null;
  }
  return { table: m[1], columns: m[2].split(',').map((c) => c.trim()) };
}

function parseSelect(sql) {
  const m = sql.match(/SELECT\s+([\w,\s*]+)\s+FROM\s+(\w+)(?:\s+WHERE\s+([\s\S]+?))?(?:\s+ORDER BY[\s\S]*)?$/i);
  if (!m) {
    return null;
  }
  const selectCols = m[1].split(',').map((c) => c.trim());
  const table = m[2];
  const whereClause = m[3] || '';
  const whereCols = [...whereClause.matchAll(/(\w+)\s*=\s*\?/g)].map((mm) => mm[1]);
  return { table, selectCols, whereCols };
}

function executeOne(statement) {
  const [sql, ...params] = Array.isArray(statement) ? statement : [statement];
  const trimmed = sql.trim();

  const createTable = parseCreateTable(trimmed);
  if (createTable) {
    if (!fs.existsSync(tablePath(createTable))) {
      saveTable(createTable, { nextId: 1, rows: [] });
    }
    return {};
  }

  const insert = parseInsert(trimmed);
  if (insert) {
    const data = loadTable(insert.table);
    const row = { id: data.nextId };
    insert.columns.forEach((col, i) => {
      row[col] = params[i];
    });
    data.rows.push(row);
    data.nextId += 1;
    saveTable(insert.table, data);
    return {};
  }

  throw new Error(`rqlite-stub: unsupported execute statement: ${trimmed.slice(0, 120)}`);
}

/** Stub replacement for `rqliteExecute` -- runs each statement against the local table engine (see module header for the two supported shapes). */
export function stubExecute(statements) {
  const results = statements.map((statement) => executeOne(statement));
  return { results };
}

/** Stub replacement for `rqliteQuery` -- returns the same `{columns, values}` shape rqlite's `/db/query` response carries. */
export function stubQuery(statement) {
  const [sql, ...params] = Array.isArray(statement) ? statement : [statement];
  const parsed = parseSelect(sql.trim());
  if (!parsed) {
    throw new Error(`rqlite-stub: unsupported query statement: ${sql.slice(0, 120)}`);
  }

  const data = loadTable(parsed.table);
  let rows = data.rows;
  parsed.whereCols.forEach((col, i) => {
    rows = rows.filter((r) => String(r[col]) === String(params[i]));
  });
  rows = rows.slice().sort((a, b) => a.id - b.id);

  const columns =
    parsed.selectCols.length === 1 && parsed.selectCols[0] === '*'
      ? Object.keys(rows[0] || { id: 0 })
      : parsed.selectCols;
  const values = rows.map((r) => columns.map((c) => r[c]));
  return { columns, values };
}
