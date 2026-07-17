package facts

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/rqlite/gorqlite"
)

// RqliteStore is the networked facts store: the identical SQLite+FTS5 schema
// and statements as SQLiteStore (see schema.go), executed over rqlite's HTTP
// API and replicated via Raft (ADR-0004). It is selected in production via
// RQLITE_URL; the embedded SQLiteStore remains the local-first default.
type RqliteStore struct {
	conn *gorqlite.Connection
	now  func() time.Time
}

// OpenRqlite connects to an rqlite node at connURL (e.g.
// "http://localhost:4001", optionally with user:pass) and applies the schema.
func OpenRqlite(connURL string) (*RqliteStore, error) {
	conn, err := gorqlite.Open(connURL)
	if err != nil {
		return nil, fmt.Errorf("facts: open rqlite %q: %w", redactURL(connURL), err)
	}
	// Weak consistency for reads: served by the leader with a small staleness
	// window — the right default for hot-path memory hydration (ADR-0004 open
	// question #1). A critical read (e.g. a supersede reconciliation) can be
	// promoted per-call later.
	if err := conn.SetConsistencyLevel(gorqlite.ConsistencyLevelWeak); err != nil {
		conn.Close()
		return nil, fmt.Errorf("facts: set consistency: %w", err)
	}
	s := &RqliteStore{conn: conn, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// migrate applies the canonical schema, idempotently, across re-boots.
//
// rqlite runs the statements of ONE request inside a single transaction, so a
// failure in any statement aborts the whole request and surfaces at the request
// level (the WriteContext error) — NOT per-statement. The canonical schema's
// only non-idempotent statement is the `ALTER TABLE facts ADD COLUMN`, which
// errors "duplicate column name" on every boot after the first. Bundling it with
// the rest (the old behaviour) meant that expected error aborted the entire
// migration at the request level, where the per-statement duplicate-column guard
// could never see it — so every re-boot against a networked rqlite failed.
//
// Fix: run each statement in its own request, in schema order, tolerating the
// duplicate-column error at BOTH the request and per-statement level. Every
// statement is individually idempotent (`CREATE ... IF NOT EXISTS`, or the
// guarded ADD COLUMN), so dropping the single-transaction bundling is safe: a
// crash mid-migration leaves an incomplete-but-valid schema that the next boot
// completes (self-healing), rather than a corrupt partial state.
func (s *RqliteStore) migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if err := s.execToleratingDupColumn(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// execToleratingDupColumn runs one schema statement as its own rqlite request
// and treats a duplicate-column error — the expected result of the idempotent
// `ALTER TABLE ADD COLUMN` re-running on an already-migrated store — as success,
// whether rqlite reports it as a transaction-aborting request-level error or
// per-statement. Any other error is a real migration failure.
func (s *RqliteStore) execToleratingDupColumn(ctx context.Context, stmt string) error {
	results, err := s.conn.WriteContext(ctx, []string{stmt})
	if err != nil {
		if isDuplicateColumnErr(err) {
			return nil
		}
		return fmt.Errorf("facts: rqlite migrate: %w", err)
	}
	for _, r := range results {
		if r.Err != nil && !isDuplicateColumnErr(r.Err) {
			return fmt.Errorf("facts: rqlite migrate statement: %w", r.Err)
		}
	}
	return nil
}

// Save upserts f. See Store.Save.
func (s *RqliteStore) Save(ctx context.Context, sc Scope, f Fact) error {
	if f.TenantID != sc.Tenant() || f.UserID != sc.User() {
		return ErrScopeMismatch
	}
	now := s.now()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now

	wr, err := s.conn.WriteOneParameterizedContext(ctx, gorqlite.ParameterizedStatement{
		Query: upsertSQL,
		// Milliseconds, not nanoseconds: rqlite round-trips integers through
		// JSON (float64), which can't hold a nanosecond epoch exactly.
		Arguments: []any{f.ID, f.TenantID, f.UserID, f.Content, ftsIndexText(f.Content), f.Importance,
			f.CreatedAt.UnixMilli(), f.UpdatedAt.UnixMilli(), f.SupersededBy},
	})
	if err != nil {
		return fmt.Errorf("facts: rqlite save %q: %w", f.ID, err)
	}
	if wr.Err != nil {
		return fmt.Errorf("facts: rqlite save %q: %w", f.ID, wr.Err)
	}
	return nil
}

// Search runs the FTS5 match over rqlite, then re-ranks in Go. See Store.Search.
func (s *RqliteStore) Search(ctx context.Context, sc Scope, query string, limit int) ([]ScoredFact, error) {
	match := ftsQuery(query)
	if match == "" || limit <= 0 {
		return nil, nil
	}
	pool := limit * candidatePoolFactor
	if pool < 20 {
		pool = 20
	}

	qr, err := s.conn.QueryOneParameterizedContext(ctx, gorqlite.ParameterizedStatement{
		Query:     searchSQL,
		Arguments: []any{match, sc.Tenant(), sc.User(), pool},
	})
	if err != nil {
		return nil, fmt.Errorf("facts: rqlite search: %w", err)
	}

	now := s.now()
	var scored []ScoredFact
	for qr.Next() {
		var (
			f          Fact
			createdMS  int64
			updatedMS  int64
			importance float64
			bm25       float64
		)
		if err := qr.Scan(&f.ID, &f.TenantID, &f.UserID, &f.Content, &importance,
			&createdMS, &updatedMS, &f.SupersededBy, &bm25); err != nil {
			return nil, fmt.Errorf("facts: rqlite scan: %w", err)
		}
		f.Importance = importance
		f.CreatedAt = time.UnixMilli(createdMS)
		f.UpdatedAt = time.UnixMilli(updatedMS)
		scored = append(scored, ScoredFact{Fact: f, Score: score(bm25, f.Importance, now.Sub(f.UpdatedAt))})
	}
	sortByScoreDesc(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// Supersede archives oldID and saves newFact in a single rqlite request, which
// rqlite executes as one transaction — so the archive and the replacement
// commit together or not at all. See Store.Supersede.
func (s *RqliteStore) Supersede(ctx context.Context, sc Scope, oldID string, newFact Fact) error {
	if newFact.TenantID != sc.Tenant() || newFact.UserID != sc.User() {
		return ErrScopeMismatch
	}
	now := s.now()
	if newFact.CreatedAt.IsZero() {
		newFact.CreatedAt = now
	}
	newFact.UpdatedAt = now

	results, err := s.conn.WriteParameterizedContext(ctx, []gorqlite.ParameterizedStatement{
		{
			Query:     markSupersededSQL,
			Arguments: []any{newFact.ID, now.UnixMilli(), oldID, sc.Tenant(), sc.User()},
		},
		{
			Query: upsertSQL,
			Arguments: []any{newFact.ID, newFact.TenantID, newFact.UserID, newFact.Content, ftsIndexText(newFact.Content), newFact.Importance,
				newFact.CreatedAt.UnixMilli(), newFact.UpdatedAt.UnixMilli(), newFact.SupersededBy},
		},
	})
	if err != nil {
		return fmt.Errorf("facts: rqlite supersede: %w", err)
	}
	for _, r := range results {
		if r.Err != nil {
			return fmt.Errorf("facts: rqlite supersede statement: %w", r.Err)
		}
	}
	// The UPDATE must have hit exactly one current, in-scope row; zero means
	// the target was absent, already superseded, or out of scope.
	if len(results) > 0 && results[0].RowsAffected == 0 {
		return ErrFactNotFound
	}
	return nil
}

// ExportAll dumps every current fact across all tenants. See Store.ExportAll.
func (s *RqliteStore) ExportAll(ctx context.Context) ([]Fact, error) {
	qr, err := s.conn.QueryOneParameterizedContext(ctx, gorqlite.ParameterizedStatement{Query: exportAllSQL})
	if err != nil {
		return nil, fmt.Errorf("facts: rqlite export: %w", err)
	}
	var out []Fact
	for qr.Next() {
		var (
			f          Fact
			createdMS  int64
			updatedMS  int64
			importance float64
		)
		if err := qr.Scan(&f.ID, &f.TenantID, &f.UserID, &f.Content, &importance,
			&createdMS, &updatedMS, &f.SupersededBy); err != nil {
			return nil, fmt.Errorf("facts: rqlite export scan: %w", err)
		}
		f.Importance = importance
		f.CreatedAt = time.UnixMilli(createdMS)
		f.UpdatedAt = time.UnixMilli(updatedMS)
		out = append(out, f)
	}
	return out, nil
}

// Delete removes the given ids within sc in one rqlite request (transaction).
// See Store.Delete.
func (s *RqliteStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	stmts := make([]gorqlite.ParameterizedStatement, 0, len(ids))
	for _, id := range ids {
		stmts = append(stmts, gorqlite.ParameterizedStatement{
			Query:     deleteSQL,
			Arguments: []any{sc.Tenant(), sc.User(), id},
		})
	}
	results, err := s.conn.WriteParameterizedContext(ctx, stmts)
	if err != nil {
		return fmt.Errorf("facts: rqlite delete: %w", err)
	}
	for _, r := range results {
		if r.Err != nil {
			return fmt.Errorf("facts: rqlite delete statement: %w", r.Err)
		}
	}
	return nil
}

// EraseScope deletes every fact — current and superseded — for sc's
// (tenant,user) in one rqlite request, returning the number of rows removed.
// See facts.ScopeEraser. The scope predicate is the isolation gate; idempotent
// (a subject with no rows returns (0, nil)).
func (s *RqliteStore) EraseScope(ctx context.Context, sc Scope) (int, error) {
	wr, err := s.conn.WriteOneParameterizedContext(ctx, gorqlite.ParameterizedStatement{
		Query:     eraseScopeSQL,
		Arguments: []any{sc.Tenant(), sc.User()},
	})
	if err != nil {
		return 0, fmt.Errorf("facts: rqlite erase scope: %w", err)
	}
	if wr.Err != nil {
		return 0, fmt.Errorf("facts: rqlite erase scope: %w", wr.Err)
	}
	return int(wr.RowsAffected), nil
}

// Close closes the HTTP connection to rqlite.
func (s *RqliteStore) Close() error {
	s.conn.Close()
	return nil
}

// redactURL strips any userinfo (credentials) from connURL before it appears
// in an error message or log, so a password embedded in the DSN never leaks.
func redactURL(connURL string) string {
	u, err := url.Parse(connURL)
	if err != nil {
		return "<rqlite>"
	}
	if u.User != nil {
		u.User = url.User("xxx")
	}
	return u.Redacted()
}
