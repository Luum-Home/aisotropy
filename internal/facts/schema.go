package facts

// schemaStatements is the canonical facts schema, shared by every Store
// implementation so the embedded (SQLite) and networked (rqlite) backends can
// never drift apart. Each statement is independent and idempotent
// (IF NOT EXISTS, or a duplicate-column-tolerant ALTER), so migrate is safe to
// run on every start.
//
// The FTS5 index is external-content (content='facts'): it stores no copy of
// the text itself, only the inverted index, and three triggers keep it in sync
// with the base table. bm25(facts_fts) supplies the relevance factor.
//
// Language-aware tokenization (ADR-0051): the index is fed from the separate
// `fts_content` column, NOT the display `content` column. `fts_content` is the
// language-preprocessed form of `content`, computed in Go by ftsIndexText
// before every write (so both the pure-Go SQLite and networked rqlite backends
// stay byte-identical without needing a custom SQL tokenizer). The tokenizer is
// `unicode61 remove_diacritics 2` — we dropped `porter` because that English
// stemmer mis-stems every non-English Latin token; `remove_diacritics 2`
// accent-folds Latin/Cyrillic/Greek so inflected accented forms still match.
// unicode61 cannot word-segment space-less scripts (CJK/Japanese/Thai), and
// modernc.org/sqlite is pure-Go and does NOT link the ICU tokenizer, so
// ftsIndexText re-expresses those runs as character bigrams before indexing;
// ftsQuery applies the identical bigram transform at query time.
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
	// fts_content holds the language-preprocessed text the FTS index tokenizes
	// (see ftsIndexText). Added via a duplicate-column-tolerant ALTER so a
	// facts.db created before ADR-0051 gains the column on next start; migrate
	// swallows the "duplicate column name" error when it already exists.
	`ALTER TABLE facts ADD COLUMN fts_content TEXT NOT NULL DEFAULT '';`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
	fts_content,
	content='facts',
	content_rowid='rowid',
	tokenize='unicode61 remove_diacritics 2'
);`,
	`CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN
	INSERT INTO facts_fts(rowid, fts_content) VALUES (new.rowid, new.fts_content);
END;`,
	`CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN
	INSERT INTO facts_fts(facts_fts, rowid, fts_content) VALUES ('delete', old.rowid, old.fts_content);
END;`,
	`CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE ON facts BEGIN
	INSERT INTO facts_fts(facts_fts, rowid, fts_content) VALUES ('delete', old.rowid, old.fts_content);
	INSERT INTO facts_fts(rowid, fts_content) VALUES (new.rowid, new.fts_content);
END;`,
}

// The parameterized SQL shared verbatim by both backends. Keeping these as
// package constants (rather than inline strings) is what lets the SQLite and
// rqlite stores issue byte-identical statements.
const (
	// upsertSQL idempotently inserts or replaces a fact by id. created_at is
	// deliberately absent from the UPDATE set so it survives an upsert.
	// fts_content (the language-preprocessed index source, ftsIndexText) travels
	// as its own bound parameter and is kept in the UPDATE set so the AFTER
	// UPDATE trigger re-indexes from the fresh value, never a stale one.
	upsertSQL = `INSERT INTO facts (id, tenant_id, user_id, content, fts_content, importance, created_at, updated_at, superseded_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	content       = excluded.content,
	fts_content   = excluded.fts_content,
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

	// eraseScopeSQL removes EVERY fact — current and superseded — for a
	// (tenant,user), the subject-erasure primitive behind GDPR Art.17
	// (ScopeEraser). Unlike deleteSQL it is not keyed by id: it clears the whole
	// subject in one statement. The scope predicate is the isolation gate, and
	// the FTS5 delete trigger keeps the external-content index in sync row by row.
	eraseScopeSQL = `DELETE FROM facts WHERE tenant_id = ? AND user_id = ?;`
)
