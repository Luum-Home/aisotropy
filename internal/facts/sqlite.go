package facts

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (BSD-3), FTS5 enabled
)

// recencyHalfLife is the half-life of the recency factor in scoring: a fact
// last updated one half-life ago contributes 0.5× its relevance×importance.
// 30 days keeps recent episodes prominent without erasing older hard
// preferences (importance carries those).
const recencyHalfLife = 30 * 24 * time.Hour

// candidatePoolFactor over-fetches FTS candidates before the Go-side
// Relevance×Importance×Recency re-ranking, so a strong-but-old or
// weak-but-important fact isn't cut by the raw bm25 order alone.
const candidatePoolFactor = 4

// SQLiteStore is the embedded, pure-Go (modernc.org/sqlite) facts store. It is
// the local-first default and the substrate the tests exercise; RqliteStore
// runs the identical schema over rqlite's HTTP API.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time // injectable clock; defaults to time.Now
}

// OpenSQLite opens (or creates) a SQLite facts store at dsn and applies the
// schema. dsn is a file path (e.g. "/var/lib/aisotropy/facts.db") or ":memory:"
// for an ephemeral store. In-memory stores are pinned to a single connection so
// every query sees the same database.
func OpenSQLite(dsn string) (*SQLiteStore, error) {
	inMemory := dsn == ":memory:" || strings.Contains(dsn, ":memory:")

	// busy_timeout avoids immediate SQLITE_BUSY under the concurrent
	// read (hydration) + write (async extraction) access pattern.
	openDSN := dsn
	if !strings.Contains(openDSN, "?") {
		openDSN += "?_pragma=busy_timeout(5000)"
	}

	db, err := sql.Open("sqlite", openDSN)
	if err != nil {
		return nil, fmt.Errorf("facts: open sqlite %q: %w", dsn, err)
	}
	if inMemory {
		// An in-memory database lives inside its connection; one connection
		// keeps all statements on the same database.
		db.SetMaxOpenConns(1)
	} else {
		// WAL lets a reader (hydration) and the writer (extraction) proceed
		// without blocking each other on a file-backed database.
		if _, err := db.ExecContext(context.Background(), `PRAGMA journal_mode=WAL;`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("facts: enable WAL: %w", err)
		}
	}

	s := &SQLiteStore{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate applies the canonical schema (facts table, FTS5 external-content
// index, sync triggers) statement by statement. Idempotent (IF NOT EXISTS).
func (s *SQLiteStore) migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("facts: migrate schema: %w", err)
		}
	}
	return nil
}

// Save upserts f. See Store.Save.
func (s *SQLiteStore) Save(ctx context.Context, sc Scope, f Fact) error {
	return s.save(ctx, s.db, sc, f)
}

// save performs the upsert against any execer (the DB or an open tx), so
// Supersede can reuse it inside a transaction.
func (s *SQLiteStore) save(ctx context.Context, ex execer, sc Scope, f Fact) error {
	if f.TenantID != sc.TenantID || f.UserID != sc.UserID {
		return ErrScopeMismatch
	}
	now := s.now()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now

	// Timestamps are stored as unix milliseconds, not nanoseconds: the rqlite
	// backend round-trips integers through JSON (float64), whose exact-integer
	// range (2^53) can't hold a nanosecond epoch. Milliseconds stay exact and
	// keep both backends byte-identical. ms precision is ample for a 30-day
	// recency half-life.
	_, err := ex.ExecContext(ctx, upsertSQL,
		f.ID, f.TenantID, f.UserID, f.Content, f.Importance,
		f.CreatedAt.UnixMilli(), f.UpdatedAt.UnixMilli(), f.SupersededBy,
	)
	if err != nil {
		return fmt.Errorf("facts: save %q: %w", f.ID, err)
	}
	return nil
}

// Search runs the FTS5 match, then re-ranks in Go. See Store.Search.
func (s *SQLiteStore) Search(ctx context.Context, sc Scope, query string, limit int) ([]ScoredFact, error) {
	match := ftsQuery(query)
	if match == "" || limit <= 0 {
		return nil, nil
	}
	pool := limit * candidatePoolFactor
	if pool < 20 {
		pool = 20
	}

	rows, err := s.db.QueryContext(ctx, searchSQL, match, sc.TenantID, sc.UserID, pool)
	if err != nil {
		return nil, fmt.Errorf("facts: search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	now := s.now()
	var scored []ScoredFact
	for rows.Next() {
		var (
			f         Fact
			createdMS int64
			updatedMS int64
			bm25      float64
		)
		if err := rows.Scan(&f.ID, &f.TenantID, &f.UserID, &f.Content, &f.Importance,
			&createdMS, &updatedMS, &f.SupersededBy, &bm25); err != nil {
			return nil, fmt.Errorf("facts: scan: %w", err)
		}
		f.CreatedAt = time.UnixMilli(createdMS)
		f.UpdatedAt = time.UnixMilli(updatedMS)
		scored = append(scored, ScoredFact{Fact: f, Score: score(bm25, f.Importance, now.Sub(f.UpdatedAt))})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("facts: rows: %w", err)
	}

	// Re-rank by the composite score (bm25 gave the candidate pool; the
	// product Relevance×Importance×Recency gives the final order).
	sortByScoreDesc(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// Supersede archives oldID and saves newFact atomically. See Store.Supersede.
func (s *SQLiteStore) Supersede(ctx context.Context, sc Scope, oldID string, newFact Fact) error {
	if newFact.TenantID != sc.TenantID || newFact.UserID != sc.UserID {
		return ErrScopeMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("facts: begin supersede tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	// Only a current fact within scope may be superseded. The scope filter is
	// part of the WHERE — the isolation gate applies to writes too.
	res, err := tx.ExecContext(ctx, markSupersededSQL,
		newFact.ID, s.now().UnixMilli(), oldID, sc.TenantID, sc.UserID)
	if err != nil {
		return fmt.Errorf("facts: mark superseded: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("facts: supersede rows: %w", err)
	}
	if n == 0 {
		return ErrFactNotFound
	}

	if err := s.save(ctx, tx, sc, newFact); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("facts: commit supersede: %w", err)
	}
	return nil
}

// ExportAll dumps every current fact across all tenants. See Store.ExportAll.
func (s *SQLiteStore) ExportAll(ctx context.Context) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx, exportAllSQL)
	if err != nil {
		return nil, fmt.Errorf("facts: export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Fact
	for rows.Next() {
		var (
			f         Fact
			createdMS int64
			updatedMS int64
		)
		if err := rows.Scan(&f.ID, &f.TenantID, &f.UserID, &f.Content, &f.Importance,
			&createdMS, &updatedMS, &f.SupersededBy); err != nil {
			return nil, fmt.Errorf("facts: export scan: %w", err)
		}
		f.CreatedAt = time.UnixMilli(createdMS)
		f.UpdatedAt = time.UnixMilli(updatedMS)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("facts: export rows: %w", err)
	}
	return out, nil
}

// Delete removes the given ids within sc, in one transaction. See Store.Delete.
func (s *SQLiteStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("facts: begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, deleteSQL, sc.TenantID, sc.UserID, id); err != nil {
			return fmt.Errorf("facts: delete %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("facts: commit delete: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// execer is the subset of *sql.DB and *sql.Tx that save needs.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// score combines the FTS5 bm25 relevance, the fact's importance, and a recency
// decay into the ranking key. bm25 is negative (more negative = better match),
// so relevance goodness is -bm25, clamped above zero so a marginal match does
// not collapse the whole product to zero.
func score(bm25, importance float64, age time.Duration) float64 {
	relevance := -bm25
	if relevance < 1e-6 {
		relevance = 1e-6
	}
	ageHalfLives := age.Seconds() / recencyHalfLife.Seconds()
	if ageHalfLives < 0 {
		ageHalfLives = 0 // a clock skew into the future is treated as "now"
	}
	recency := math.Exp2(-ageHalfLives)
	return relevance * importance * recency
}

// sortByScoreDesc orders scored facts by Score descending, breaking ties by
// UpdatedAt (newer first) for a deterministic order.
func sortByScoreDesc(fs []ScoredFact) {
	// Insertion sort keeps it dependency-free and is ample for the small
	// candidate pool (tens of rows at most).
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && less(fs[j-1], fs[j]); j-- {
			fs[j-1], fs[j] = fs[j], fs[j-1]
		}
	}
}

// less reports whether a should sort AFTER b (lower rank): smaller score, or
// equal score and older update.
func less(a, b ScoredFact) bool {
	if a.Score != b.Score {
		return a.Score < b.Score
	}
	return a.UpdatedAt.Before(b.UpdatedAt)
}

// ftsQuery turns arbitrary user text into a safe FTS5 MATCH expression: it
// extracts word tokens and OR-joins them as quoted terms, so punctuation or
// FTS5 operator characters in the input can never produce a syntax error or
// injection. Returns "" when the input has no usable tokens.
func ftsQuery(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 { // drop single-character noise
			continue
		}
		terms = append(terms, `"`+strings.ToLower(f)+`"`)
	}
	return strings.Join(terms, " OR ")
}
