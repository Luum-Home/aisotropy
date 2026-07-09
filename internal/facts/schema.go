package facts

// schemaStatements is the canonical facts schema, shared by every Store
// implementation so the embedded (SQLite) and networked (rqlite) backends can
// never drift apart. Each statement is independent and idempotent
// (IF NOT EXISTS), so migrate is safe to run on every start.
//
// The FTS5 index is external-content (content='facts'): it stores no copy of
// the text itself, only the inverted index, and three triggers keep it in sync
// with the base table. bm25(facts_fts) supplies the relevance factor.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS facts (
	id            TEXT PRIMARY KEY,
	tenant_id     TEXT NOT NULL,
	user_id       TEXT NOT NULL,
	content       TEXT NOT NULL,
	importance    REAL NOT NULL DEFAULT 0.5,
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	superseded_by TEXT NOT NULL DEFAULT ''
);`,
	`CREATE INDEX IF NOT EXISTS idx_facts_scope ON facts(tenant_id, user_id, superseded_by);`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
	content,
	content='facts',
	content_rowid='rowid',
	tokenize='porter unicode61'
);`,
	`CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN
	INSERT INTO facts_fts(rowid, content) VALUES (new.rowid, new.content);
END;`,
	`CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN
	INSERT INTO facts_fts(facts_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
END;`,
	`CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE ON facts BEGIN
	INSERT INTO facts_fts(facts_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
	INSERT INTO facts_fts(rowid, content) VALUES (new.rowid, new.content);
END;`,
}

// The parameterized SQL shared verbatim by both backends. Keeping these as
// package constants (rather than inline strings) is what lets the SQLite and
// rqlite stores issue byte-identical statements.
const (
	// upsertSQL idempotently inserts or replaces a fact by id. created_at is
	// deliberately absent from the UPDATE set so it survives an upsert.
	upsertSQL = `INSERT INTO facts (id, tenant_id, user_id, content, importance, created_at, updated_at, superseded_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	content       = excluded.content,
	importance    = excluded.importance,
	updated_at    = excluded.updated_at,
	superseded_by = excluded.superseded_by;`

	// searchSQL fetches the FTS candidate pool for a scope, ordered by raw
	// bm25 relevance; the composite Relevance×Importance×Recency re-ranking
	// happens in Go.
	searchSQL = `SELECT f.id, f.tenant_id, f.user_id, f.content, f.importance, f.created_at, f.updated_at, f.superseded_by,
       bm25(facts_fts) AS relevance
FROM facts_fts
JOIN facts f ON f.rowid = facts_fts.rowid
WHERE facts_fts MATCH ?
  AND f.tenant_id = ? AND f.user_id = ? AND f.superseded_by = ''
ORDER BY relevance ASC
LIMIT ?;`

	// markSupersededSQL archives a current fact within scope. RowsAffected==0
	// means the fact was absent, already superseded, or out of scope.
	markSupersededSQL = `UPDATE facts SET superseded_by = ?, updated_at = ?
WHERE id = ? AND tenant_id = ? AND user_id = ? AND superseded_by = '';`

	// exportAllSQL dumps every current fact across all tenants for the offline
	// sleep-cycle batch (the one reviewed cross-tenant read; it re-partitions by
	// tenant_id downstream).
	exportAllSQL = `SELECT id, tenant_id, user_id, content, importance, created_at, updated_at, superseded_by
FROM facts WHERE superseded_by = '' ORDER BY id;`

	// deleteSQL removes one fact by id within scope (isolation gate on the
	// cleanup write). Unknown ids no-op.
	deleteSQL = `DELETE FROM facts WHERE tenant_id = ? AND user_id = ? AND id = ?;`
)
