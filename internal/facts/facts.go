// Package facts is the Aisotropy facts-memory tier — the Disk/SSD layer of the
// LLM OS (docs/components/rqlite-facts-memory.md): an Engram-style journal of
// distilled, durable statements about a user (preferences, decisions, rules,
// summarized episodes), retrieved by keyword (SQLite FTS5) rather than by
// vector similarity — the direct application of the KB-over-RAG principle.
//
// The substrate is SQLite + FTS5. Two Store implementations share the exact
// same schema and queries:
//
//   - SQLiteStore: embedded, pure-Go (modernc.org/sqlite), local-first, the
//     zero-config default and the substrate exercised by the tests.
//   - RqliteStore: the same SQLite distributed over rqlite's HTTP API
//     (ADR-0004), for networked, Raft-replicated deployments.
//
// Multi-tenant isolation is a constitutional gate: every read and write
// filters by (tenant_id, user_id). Isolation lives in every statement, never
// in an intermediate layer (see .claude/rules/constitutional-gates.md and
// docs/vision/principles.md).
package facts

import (
	"context"
	"errors"
	"time"
)

// ErrScopeMismatch is returned when a Fact's TenantID/UserID does not match the
// Scope a Store operation runs under — a caller trying to write cross-tenant
// data, which the isolation gate forbids.
var ErrScopeMismatch = errors.New("facts: fact tenant/user does not match the operation scope")

// ErrFactNotFound is returned by Supersede when the target fact does not exist
// within the given scope (or was already superseded).
var ErrFactNotFound = errors.New("facts: fact not found in scope")

// Scope identifies the (tenant, user) a query is confined to. Every Store
// operation is scoped; it is derived once at the Huma handler boundary from
// the validated path params (mirroring memory.Key), so tenant identity still
// originates from ctx and never from an untrusted body/header.
type Scope struct {
	TenantID string
	UserID   string
}

// Fact is one Engram-style memory. Content is distilled text (never raw chat)
// and is what the FTS5 index matches against.
type Fact struct {
	// ID is a stable idempotency key. Re-saving the same ID upserts in place,
	// so an Outbox worker (Phase 5) may retry a write without duplicating.
	ID string
	// TenantID and UserID scope the fact; they must match the Scope of any
	// operation that touches it.
	TenantID string
	UserID   string
	// Content is the distilled statement, indexed by FTS5.
	Content string
	// Importance is a heuristic weight in [0,1] used as a factor in scoring.
	Importance float64
	// CreatedAt is when the fact was first stored; preserved across upserts.
	CreatedAt time.Time
	// UpdatedAt is the last write; drives the recency factor in scoring.
	UpdatedAt time.Time
	// SupersededBy is the ID of the fact that replaced this one, or "" while
	// the fact is current. Superseded facts are kept for audit but excluded
	// from Search.
	SupersededBy string
}

// ScoredFact is a Fact returned from Search, annotated with the score that
// ranked it: Relevance (FTS5 bm25) × Importance × Recency.
type ScoredFact struct {
	Fact
	Score float64
}

// Store is the facts-memory engine. Implementations MUST filter every read and
// write by (tenant_id, user_id) — the isolation gate — and MUST make Save
// idempotent on Fact.ID (UPSERT).
type Store interface {
	// Save inserts f, or idempotently replaces the row with the same ID
	// (content/importance/updated_at/superseded_by are refreshed; created_at
	// is preserved). Returns ErrScopeMismatch if f.TenantID/f.UserID differ
	// from sc.
	Save(ctx context.Context, sc Scope, f Fact) error

	// Search returns up to limit current (non-superseded) facts for sc whose
	// content matches query under FTS5, ranked by Relevance × Importance ×
	// Recency, highest first. An empty/whitespace query yields no results.
	Search(ctx context.Context, sc Scope, query string, limit int) ([]ScoredFact, error)

	// Supersede marks oldID (within sc) as replaced by newFact and saves
	// newFact, atomically. Returns ErrFactNotFound if oldID is not a current
	// fact in sc. The old fact remains for audit but is excluded from Search.
	Supersede(ctx context.Context, sc Scope, oldID string, newFact Fact) error

	// Close releases the underlying handle (DB / HTTP client).
	Close() error
}

// Turn is a completed conversational exchange handed to an Extractor after the
// stream finishes.
type Turn struct {
	UserMessage      string
	AssistantMessage string
}

// Extractor turns a completed Turn into zero or more candidate facts to
// persist. Phase 3 ships the deterministic HeuristicExtractor; richer,
// LLM/DataFusion-driven distillation (dedup, contradiction resolution) is
// deferred to the sleep cycle (Phase 6). An Extractor MUST be cheap and
// side-effect-free: it runs inline in the post-response goroutine.
type Extractor interface {
	Extract(sc Scope, turn Turn) []Fact
}
