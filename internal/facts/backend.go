package facts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DocBackend is the NARROW, tenant-UNAWARE driver a community author implements
// to plug a third-party keyword/document engine into Aisotropy's facts tier —
// "bring your own backend, NOT your isolation" (Tier-2 open core).
//
// Like semantic.VectorBackend, it has NO concept of tenant, user, or Scope. It
// stores opaque documents under an OPAQUE namespace and resolves keyword search
// within a namespace. All multi-tenant isolation — the constitutional gate —
// is supplied by the SEALED decorator in this package (NewDocBackendStore),
// which derives the namespace from a validated (tenant,user) Scope and enforces
// it on every scoped call. Because that derivation (namespaceFor) is
// UNEXPORTED, a community backend can never see or choose the tenant, so a
// buggy backend cannot widen a search past its namespace.
//
// The one deliberate exception is ExportAll: it is the sleep-cycle's reviewed
// CROSS-TENANT read (mirroring facts.Store.ExportAll), so it enumerates every
// namespace's documents. Identity for those rows is reconstructed from the
// document's stored fields, never a scope — this is the single audited breach
// of the per-call scoping, exactly as the SQLite backend's exportAllSQL is.
//
// A backend is exercised for conformance (round-trip, CRUD, keyword search, and
// the central isolation-seal proof) by internal/plugintest.RunDocBackendSuite.
type DocBackend interface {
	// Put inserts or replaces the document keyed (ns, key), idempotently on
	// key. The backend persists Content (keyword-searchable) and the opaque
	// Fields map verbatim.
	Put(ctx context.Context, ns, key string, doc BackendDoc) error

	// Get returns the document keyed (ns, key); ok is false if it is absent.
	Get(ctx context.Context, ns, key string) (BackendDoc, bool, error)

	// Search returns up to k documents within ns whose Content matches the
	// keyword query, best (highest relevance) first. Only documents written
	// under the SAME ns may be returned.
	Search(ctx context.Context, ns, query string, k int) ([]BackendDocHit, error)

	// Delete removes the given keys within ns. Unknown keys are ignored.
	Delete(ctx context.Context, ns string, keys []string) error

	// Scan returns every document under ns; it backs the decorator's
	// per-namespace maintenance reads.
	Scan(ctx context.Context, ns string) ([]BackendDoc, error)

	// ExportAll returns every document across ALL namespaces — the sleep-cycle
	// cross-tenant export path only. The decorator reconstructs each row's
	// tenant/user from the document's own fields, so a backend MUST round-trip
	// the identity fields it was given at Put verbatim.
	ExportAll(ctx context.Context) ([]BackendDoc, error)

	// Close releases the backend's resources.
	Close() error
}

// BackendDoc is one opaque stored fact document: an idempotency key, the
// keyword-searchable content, and an opaque string->string Fields map the
// decorator round-trips (importance, timestamps, superseded_by, and — for the
// ExportAll cross-tenant path only — tenant/user). The backend treats Content
// as searchable text and Fields as an opaque blob.
type BackendDoc struct {
	// Key is the decorator-supplied idempotency key (the Fact.ID).
	Key string
	// Content is the distilled statement, matched by the backend's keyword index.
	Content string
	// Fields is opaque attribute storage the backend persists verbatim.
	Fields map[string]string
}

// BackendDocHit is a BackendDoc annotated with the backend's keyword relevance
// Score (higher = better), as returned by DocBackend.Search. The decorator
// folds this relevance into its Relevance×Importance×Recency final ranking.
type BackendDocHit struct {
	BackendDoc
	Score float64
}

// field keys the decorator uses to round-trip a Fact's attributes through a
// backend's opaque Fields map. fkTenant/fkUser are persisted ONLY so the
// cross-tenant ExportAll path can reconstruct identity with no scope in hand;
// every SCOPED operation takes identity from the Scope + namespace, never from
// these fields.
const (
	fkTenant       = "aisotropy.tenant"
	fkUser         = "aisotropy.user"
	fkImportance   = "aisotropy.importance"
	fkCreatedAt    = "aisotropy.created_at_ms"
	fkUpdatedAt    = "aisotropy.updated_at_ms"
	fkSupersededBy = "aisotropy.superseded_by"
)

// docCandidatePoolFactor over-fetches keyword candidates from a backend Search
// before the decorator re-ranks by Relevance×Importance×Recency and drops
// superseded rows, mirroring the SQLite store's candidatePoolFactor so a
// strong-but-old or weak-but-important fact is not cut by the raw keyword order.
const docCandidatePoolFactor = 4

// namespaceFor derives the OPAQUE storage namespace for a Scope from its
// (tenant,user) identity. As in the semantic tier, it is the seal of the
// Tier-2 open-core design: UNEXPORTED, deterministic, and \x00-separated so no
// tenant/user pair collides into another's bucket. A community DocBackend can
// never compute it, so it can never be handed a namespace for a tenant the
// runtime did not validate.
func namespaceFor(sc Scope) string {
	sum := sha256.Sum256([]byte(sc.tenant + "\x00" + sc.user))
	return "aisotropy-facts-v1-" + hex.EncodeToString(sum[:])
}

// scopedDocStore wraps ANY tenant-unaware DocBackend into a real facts.Store
// that satisfies the constitutional isolation gate. It is UNEXPORTED and
// constructed only via NewDocBackendStore; a consumer holds it as a facts.Store
// and can never reach the namespace derivation. Every scoped method confines
// the backend call to the caller's namespace, so cross-tenant access is
// structurally impossible.
type scopedDocStore struct {
	backend DocBackend
	now     func() time.Time // injectable clock; defaults to time.Now
}

// NewDocBackendStore seals a community-authored DocBackend into a
// tenant-isolating facts.Store. This is the ONLY blessed way to turn a
// DocBackend into a Store: there is no exported path that lets a caller bypass
// the namespace seal.
func NewDocBackendStore(backend DocBackend) Store {
	return &scopedDocStore{backend: backend, now: time.Now}
}

// Save upserts f into sc's namespace, preserving the original created_at across
// an upsert exactly like the SQLite backend (created_at is absent from the
// SQLite UPDATE set). Returns ErrScopeMismatch on a cross-tenant write.
func (s *scopedDocStore) Save(ctx context.Context, sc Scope, f Fact) error {
	if f.TenantID != sc.tenant || f.UserID != sc.user {
		return ErrScopeMismatch
	}
	return s.put(ctx, namespaceFor(sc), sc, f)
}

// put writes f under ns, preserving an existing document's created_at.
func (s *scopedDocStore) put(ctx context.Context, ns string, sc Scope, f Fact) error {
	now := s.now()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	// Preserve the original created_at on an upsert (the SQLite store does this
	// by omitting created_at from its UPDATE set).
	if existing, ok, err := s.backend.Get(ctx, ns, f.ID); err != nil {
		return err
	} else if ok {
		f.CreatedAt = millisToTime(existing.Fields[fkCreatedAt])
	}
	f.UpdatedAt = now
	return s.backend.Put(ctx, ns, f.ID, docFromFact(sc, f))
}

// Search runs the backend keyword search within sc's namespace, drops
// superseded rows, re-ranks by Relevance×Importance×Recency, and trims to
// limit. Identity on every returned fact comes from sc, never the document
// fields (the seal).
func (s *scopedDocStore) Search(ctx context.Context, sc Scope, query string, limit int) ([]ScoredFact, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	pool := limit * docCandidatePoolFactor
	if pool < 20 {
		pool = 20
	}
	hits, err := s.backend.Search(ctx, namespaceFor(sc), query, pool)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var scored []ScoredFact
	for _, h := range hits {
		f := factFromDoc(sc, h.BackendDoc)
		if f.SupersededBy != "" {
			continue // superseded facts are excluded from Search
		}
		scored = append(scored, ScoredFact{Fact: f, Score: combineDocScore(h.Score, f.Importance, now.Sub(f.UpdatedAt))})
	}
	sortScoredDesc(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// Supersede marks oldID (within sc) as replaced by newFact and saves newFact.
// A KV backend has no cross-key transaction, so the two writes are sequential
// (documented tradeoff versus the SQLite store's real tx): the archive write
// lands first, then the new fact. Returns ErrFactNotFound if oldID is not a
// current fact in sc.
func (s *scopedDocStore) Supersede(ctx context.Context, sc Scope, oldID string, newFact Fact) error {
	if newFact.TenantID != sc.tenant || newFact.UserID != sc.user {
		return ErrScopeMismatch
	}
	ns := namespaceFor(sc)
	old, ok, err := s.backend.Get(ctx, ns, oldID)
	if err != nil {
		return err
	}
	if !ok || old.Fields[fkSupersededBy] != "" {
		return ErrFactNotFound
	}
	// Archive the old fact in place (mark superseded, bump updated_at), keeping
	// its created_at and every other field.
	old.Fields[fkSupersededBy] = newFact.ID
	old.Fields[fkUpdatedAt] = strconv.FormatInt(s.now().UnixMilli(), 10)
	if err := s.backend.Put(ctx, ns, oldID, old); err != nil {
		return err
	}
	return s.put(ctx, ns, sc, newFact)
}

// ExportAll dumps every current fact across ALL tenants — the one reviewed
// cross-tenant read, backing the offline sleep-cycle consolidator. Identity is
// reconstructed from each document's stored fields (there is no scope on this
// path), and superseded rows are excluded, matching exportAllSQL. Ordered by id.
func (s *scopedDocStore) ExportAll(ctx context.Context) ([]Fact, error) {
	docs, err := s.backend.ExportAll(ctx)
	if err != nil {
		return nil, err
	}
	var out []Fact
	for _, d := range docs {
		f := factFromExportedDoc(d)
		if f.SupersededBy != "" {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Delete removes ids within sc's namespace; a colliding id under another tenant
// lives in a different namespace and is untouched.
func (s *scopedDocStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.backend.Delete(ctx, namespaceFor(sc), ids)
}

// Close releases the wrapped backend.
func (s *scopedDocStore) Close() error { return s.backend.Close() }

// docFromFact serializes a Fact into an opaque backend document. Tenant/user are
// persisted only to back the cross-tenant ExportAll path; scoped reads never
// consult them.
func docFromFact(sc Scope, f Fact) BackendDoc {
	return BackendDoc{
		Key:     f.ID,
		Content: f.Content,
		Fields: map[string]string{
			fkTenant:       sc.tenant,
			fkUser:         sc.user,
			fkImportance:   strconv.FormatFloat(f.Importance, 'g', -1, 64),
			fkCreatedAt:    strconv.FormatInt(f.CreatedAt.UnixMilli(), 10),
			fkUpdatedAt:    strconv.FormatInt(f.UpdatedAt.UnixMilli(), 10),
			fkSupersededBy: f.SupersededBy,
		},
	}
}

// factFromDoc reconstructs a Fact from a backend document on a SCOPED path,
// taking TenantID/UserID from sc — never the document fields (the seal).
func factFromDoc(sc Scope, d BackendDoc) Fact {
	return Fact{
		ID:           d.Key,
		TenantID:     sc.tenant,
		UserID:       sc.user,
		Content:      d.Content,
		Importance:   parseFloat(d.Fields[fkImportance]),
		CreatedAt:    millisToTime(d.Fields[fkCreatedAt]),
		UpdatedAt:    millisToTime(d.Fields[fkUpdatedAt]),
		SupersededBy: d.Fields[fkSupersededBy],
	}
}

// factFromExportedDoc reconstructs a Fact on the CROSS-TENANT ExportAll path,
// taking identity from the document's own fields because no scope is available.
// This is the single audited exception to scope-sourced identity, matching the
// SQLite exportAllSQL behavior.
func factFromExportedDoc(d BackendDoc) Fact {
	return Fact{
		ID:           d.Key,
		TenantID:     d.Fields[fkTenant],
		UserID:       d.Fields[fkUser],
		Content:      d.Content,
		Importance:   parseFloat(d.Fields[fkImportance]),
		CreatedAt:    millisToTime(d.Fields[fkCreatedAt]),
		UpdatedAt:    millisToTime(d.Fields[fkUpdatedAt]),
		SupersededBy: d.Fields[fkSupersededBy],
	}
}

// combineDocScore folds a backend's POSITIVE keyword relevance, the fact's
// importance, and a recency decay into the ranking key. It parallels the SQLite
// store's score(), but takes relevance already oriented as higher=better (a
// generic backend returns positive relevance, unlike SQLite's negative bm25).
func combineDocScore(relevance, importance float64, age time.Duration) float64 {
	if relevance < 1e-6 {
		relevance = 1e-6
	}
	ageHalfLives := age.Seconds() / recencyHalfLife.Seconds()
	if ageHalfLives < 0 {
		ageHalfLives = 0
	}
	recency := math.Exp2(-ageHalfLives)
	return relevance * importance * recency
}

// sortScoredDesc orders facts by Score descending, ties broken by newer
// UpdatedAt then id, for a deterministic order.
func sortScoredDesc(fs []ScoredFact) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Score != fs[j].Score {
			return fs[i].Score > fs[j].Score
		}
		if !fs[i].UpdatedAt.Equal(fs[j].UpdatedAt) {
			return fs[i].UpdatedAt.After(fs[j].UpdatedAt)
		}
		return fs[i].ID < fs[j].ID
	})
}

// parseFloat parses an importance field, yielding 0 on an empty/malformed value.
func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// millisToTime parses a unix-millis field back into a time.Time; empty or
// malformed yields the zero time.
func millisToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
