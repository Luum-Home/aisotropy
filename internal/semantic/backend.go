package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"
)

// VectorBackend is the NARROW, tenant-UNAWARE driver a community author
// implements to plug a third-party vector engine into Aisotropy's semantic
// tier — "bring your own backend, NOT your isolation" (Tier-2 open core).
//
// It deliberately has NO concept of tenant, user, or Scope. It operates on an
// OPAQUE namespace string and OPAQUE ids/payload that it MUST treat as
// black boxes: a backend MUST NOT parse a namespace, derive access control
// from a payload key, or share storage across namespaces. All multi-tenant
// isolation — the constitutional gate — is supplied by the SEALED decorator in
// this package (NewVectorBackendStore), which derives the namespace from a
// validated (tenant,user) Scope and enforces it on every call. Because that
// derivation (namespaceFor) is UNEXPORTED, a community backend can never see
// or choose the tenant, so a buggy or malicious backend cannot widen a scan
// past its namespace: the worst a broken backend can do is fail its own
// namespace's round-trip, never leak across tenants.
//
// A backend is exercised for conformance (round-trip, CRUD, and the central
// isolation-seal proof) by internal/plugintest.RunVectorBackendSuite.
type VectorBackend interface {
	// Upsert inserts or replaces items within ns, keyed by BackendVector.ID
	// (idempotent on ID). The backend stores each item's Vector and opaque
	// Payload verbatim; it must not interpret payload contents.
	Upsert(ctx context.Context, ns string, items []BackendVector) error

	// Search returns up to k items within ns most similar to query, best
	// (highest similarity) first. Only items previously written under the SAME
	// ns may be returned — returning an item from another namespace is an
	// isolation breach the decorator cannot repair.
	Search(ctx context.Context, ns string, query []float32, k int) ([]BackendHit, error)

	// Delete removes the given ids within ns. Unknown ids are ignored (no
	// error). An id that exists only under a different namespace must be left
	// untouched.
	Delete(ctx context.Context, ns string, ids []string) error

	// Scan returns every item stored under ns. It backs the decorator's
	// maintenance operations (DeleteBySource, PruneTurns) which must enumerate
	// a namespace's contents by payload attribute or age; it is never on the
	// hot search path.
	Scan(ctx context.Context, ns string) ([]BackendVector, error)

	// Close releases the backend's resources.
	Close() error
}

// BackendVector is one opaque stored item the decorator hands a VectorBackend:
// an id, its embedding, and an opaque string->string payload the decorator
// round-trips (it carries the Point's Kind/Content/Source/Citation/CreatedAt).
// The backend treats all three fields as opaque. Note that tenant/user identity
// is NOT part of the payload: identity lives only in the namespace and is
// reattached from the Scope on read, so a backend never handles it.
type BackendVector struct {
	// ID is the decorator-supplied idempotency key (the semantic Point.ID).
	ID string
	// Vector is the embedding; its length equals the store dimension.
	Vector []float32
	// Payload is opaque attribute storage the backend persists verbatim.
	Payload map[string]string
}

// BackendHit is a BackendVector annotated with its similarity Score (higher =
// closer), as returned by VectorBackend.Search.
type BackendHit struct {
	BackendVector
	Score float32
}

// payload keys the decorator uses to round-trip the non-identity fields of a
// Point through a backend's opaque payload map. Identity (tenant/user) is
// deliberately absent — it is carried by the namespace and reattached from the
// Scope on read, which is the seal.
const (
	pkKind      = "aisotropy.kind"
	pkContent   = "aisotropy.content"
	pkSource    = "aisotropy.source"
	pkCitation  = "aisotropy.citation"
	pkCreatedAt = "aisotropy.created_at_ms"
)

// backendKindPoolFactor over-fetches candidates from a backend Search before
// the decorator post-filters by Kind (Kind is a payload attribute, not part of
// the namespace, so it cannot be pushed into the backend scan). Fetching
// limit*factor candidates and keeping the first `limit` of the requested kinds
// mirrors the facts tier's candidatePoolFactor and keeps the backend's ANN
// index doing the heavy lifting instead of a full namespace Scan.
const backendKindPoolFactor = 8

// namespaceFor derives the OPAQUE storage namespace for a Scope from its
// (tenant,user) identity. It is the seal of the Tier-2 open-core design: it is
// UNEXPORTED, so no code outside package semantic — least of all a community
// VectorBackend — can compute or forge the namespace for a tenant it was not
// handed a Scope for. Every scoped operation routes through it, so a backend
// only ever sees the hash for the exact (tenant,user) the runtime validated at
// the Huma boundary. The hash is stable across processes (deterministic
// SHA-256) so a persistent backend keeps addressing the same namespace after a
// restart. The \x00 separator makes ("a","bc") and ("ab","c") distinct
// namespaces, so no tenant/user pair can collide into another's bucket.
func namespaceFor(sc Scope) string {
	sum := sha256.Sum256([]byte(sc.tenant + "\x00" + sc.user))
	return "aisotropy-sem-v1-" + hex.EncodeToString(sum[:])
}

// scopedVectorStore wraps ANY tenant-unaware VectorBackend into a real
// semantic.Store that satisfies the constitutional isolation gate. It is
// UNEXPORTED and constructed only via NewVectorBackendStore: a consumer holds
// it as a semantic.Store and can never reach the namespace derivation or feed
// the backend a namespace of its own choosing. Every method derives the
// namespace from the caller-supplied Scope with namespaceFor and confines the
// underlying backend call to it, so cross-tenant reads/writes/deletes are
// structurally impossible — a colliding id under a different tenant maps to a
// different namespace the backend was never asked about.
type scopedVectorStore struct {
	backend VectorBackend
	dim     int
}

// NewVectorBackendStore seals a community-authored VectorBackend into a
// tenant-isolating semantic.Store. dim is the embedding dimension the store
// enforces (matching your Embedder.Dimension()), so a wrong-width vector is
// rejected with ErrDimensionMismatch exactly like the built-in stores. The
// returned Store applies the (tenant,user) namespace on every call; the backend
// never learns the tenant. This is the ONLY blessed way to turn a VectorBackend
// into a Store — there is no exported path that lets a caller bypass the
// namespace seal.
func NewVectorBackendStore(backend VectorBackend, dim int) Store {
	return &scopedVectorStore{backend: backend, dim: dim}
}

// Upsert validates each point's identity against sc (ErrScopeMismatch on a
// cross-tenant write, matching InMemoryStore) and its width against the store
// dimension, then writes to the scope's namespace. Identity is NOT persisted
// into the backend payload: it is implied by the namespace, so a backend can
// never surface a wrong tenant on read.
func (s *scopedVectorStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	items := make([]BackendVector, 0, len(points))
	for _, p := range points {
		if p.TenantID != sc.tenant || p.UserID != sc.user {
			return ErrScopeMismatch
		}
		if len(p.Vector) != s.dim {
			return ErrDimensionMismatch
		}
		items = append(items, BackendVector{
			ID:      p.ID,
			Vector:  p.Vector,
			Payload: payloadFromPoint(p),
		})
	}
	if len(items) == 0 {
		return nil
	}
	return s.backend.Upsert(ctx, namespaceFor(sc), items)
}

// Search runs the backend ANN within sc's namespace and reattaches identity
// from sc on every returned point (never from payload — the seal). The optional
// Kind filter is applied in the decorator over an over-fetched candidate pool
// (Kind is a payload attribute, not part of the isolation namespace); the
// (tenant,user) filter is the namespace itself, applied during the backend
// scan, never as a post-filter.
func (s *scopedVectorStore) Search(ctx context.Context, sc Scope, query []float32, limit int, kinds ...Kind) ([]ScoredPoint, error) {
	if len(query) != s.dim {
		return nil, ErrDimensionMismatch
	}
	if limit <= 0 {
		return nil, nil
	}
	k := limit
	if len(kinds) > 0 {
		// Over-fetch so the Kind post-filter can still fill `limit`.
		k = limit * backendKindPoolFactor
	}
	hits, err := s.backend.Search(ctx, namespaceFor(sc), query, k)
	if err != nil {
		return nil, err
	}
	out := make([]ScoredPoint, 0, len(hits))
	for _, h := range hits {
		p := pointFromBackend(sc, h.BackendVector)
		if !kindAllowed(kinds, p.Kind) {
			continue
		}
		out = append(out, ScoredPoint{Point: p, Score: h.Score})
	}
	// Re-sort defensively (highest first, ties by ID) so the contract holds
	// even if a backend returns a loosely ordered pool, then trim to limit.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Delete removes ids within sc's namespace. Scope enforcement is the namespace
// itself: a colliding id belonging to another tenant lives under a different
// namespace the backend is never asked about, so it is untouched — the same
// guarantee InMemoryStore gives via its per-point tenant check.
func (s *scopedVectorStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.backend.Delete(ctx, namespaceFor(sc), ids)
}

// DeleteBySource removes every point within sc whose Kind and Source match, by
// scanning sc's namespace (never another's) and deleting the matches as a unit.
// Returns the number removed; no match is not an error.
func (s *scopedVectorStore) DeleteBySource(ctx context.Context, sc Scope, kind Kind, source string) (int, error) {
	ns := namespaceFor(sc)
	all, err := s.backend.Scan(ctx, ns)
	if err != nil {
		return 0, err
	}
	var toDelete []string
	for _, bv := range all {
		if Kind(bv.Payload[pkKind]) == kind && bv.Payload[pkSource] == source {
			toDelete = append(toDelete, bv.ID)
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	if err := s.backend.Delete(ctx, ns, toDelete); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

// PruneTurns evicts KindTurn points within sc by age and/or count cap, mirroring
// InMemoryStore.PruneTurns exactly (Document/Fact points are never touched).
// The scan is confined to sc's namespace, so another tenant's turns are
// structurally unreachable.
func (s *scopedVectorStore) PruneTurns(ctx context.Context, sc Scope, maxAge time.Duration, maxCount int) (int, error) {
	if maxAge <= 0 && maxCount <= 0 {
		return 0, nil
	}
	ns := namespaceFor(sc)
	all, err := s.backend.Scan(ctx, ns)
	if err != nil {
		return 0, err
	}
	// Collect KindTurn points with their CreatedAt for the age/count policy.
	type turn struct {
		id        string
		createdAt time.Time
	}
	var turns []turn
	for _, bv := range all {
		if Kind(bv.Payload[pkKind]) != KindTurn {
			continue
		}
		turns = append(turns, turn{id: bv.ID, createdAt: millisToTime(bv.Payload[pkCreatedAt])})
	}

	toDelete := make(map[string]struct{})
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		for _, t := range turns {
			if t.createdAt.Before(cutoff) {
				toDelete[t.id] = struct{}{}
			}
		}
	}
	if maxCount > 0 && len(turns) > maxCount {
		// Newest first, so the surviving prefix is the maxCount most recent;
		// tie-break by id for determinism.
		sort.Slice(turns, func(i, j int) bool {
			if !turns[i].createdAt.Equal(turns[j].createdAt) {
				return turns[i].createdAt.After(turns[j].createdAt)
			}
			return turns[i].id < turns[j].id
		})
		for _, t := range turns[maxCount:] {
			toDelete[t.id] = struct{}{}
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(toDelete))
	for id := range toDelete {
		ids = append(ids, id)
	}
	if err := s.backend.Delete(ctx, ns, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// Close releases the wrapped backend.
func (s *scopedVectorStore) Close() error { return s.backend.Close() }

// payloadFromPoint serializes a Point's NON-identity fields into a backend's
// opaque payload map. Tenant/user are intentionally excluded: identity is the
// namespace, reattached from the Scope on read.
func payloadFromPoint(p Point) map[string]string {
	return map[string]string{
		pkKind:      string(p.Kind),
		pkContent:   p.Content,
		pkSource:    p.Source,
		pkCitation:  p.Citation,
		pkCreatedAt: strconv.FormatInt(p.CreatedAt.UnixMilli(), 10),
	}
}

// pointFromBackend reconstructs a Point from a backend item, taking TenantID and
// UserID from sc — NEVER from the payload. This is the read half of the seal: a
// backend that (accidentally or maliciously) returned an item stored for another
// tenant would still be relabeled with the caller's scope, but it can't: the
// item came from the caller's own namespace, so its identity is correct by
// construction.
func pointFromBackend(sc Scope, bv BackendVector) Point {
	return Point{
		ID:        bv.ID,
		TenantID:  sc.tenant,
		UserID:    sc.user,
		Kind:      Kind(bv.Payload[pkKind]),
		Content:   bv.Payload[pkContent],
		Source:    bv.Payload[pkSource],
		Citation:  bv.Payload[pkCitation],
		Vector:    bv.Vector,
		CreatedAt: millisToTime(bv.Payload[pkCreatedAt]),
	}
}

// millisToTime parses a unix-millis payload string back into a time.Time; an
// empty or malformed value yields the zero time (matching a Point written
// without a CreatedAt).
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
