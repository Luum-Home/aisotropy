package semantic

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// InMemoryStore is a brute-force cosine vector index held in process. It is the
// zero-config, local-first default and the substrate the tests exercise;
// QdrantStore runs the same contract over Qdrant's gRPC API. Brute force is
// fine for the walking skeleton and small collections — the whole point of the
// Store interface is that swapping in Qdrant's HNSW is invisible to callers.
type InMemoryStore struct {
	mu     sync.RWMutex
	dim    int
	points map[string]Point // keyed by ID (UUIDv5) so upsert is idempotent
}

// NewInMemoryStore creates an empty index for vectors of the given dimension
// (match your Embedder.Dimension()).
func NewInMemoryStore(dim int) *InMemoryStore {
	return &InMemoryStore{dim: dim, points: make(map[string]Point)}
}

// Upsert stores points, replacing any with the same ID. See Store.Upsert.
func (s *InMemoryStore) Upsert(_ context.Context, sc Scope, points []Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			return ErrScopeMismatch
		}
		if len(p.Vector) != s.dim {
			return ErrDimensionMismatch
		}
		s.points[p.ID] = p // idempotent by ID: a retried event is a no-op
	}
	return nil
}

// Search returns the limit most cosine-similar points within sc. The
// (tenant,user) filter is applied inside the scan loop, so another tenant's
// vectors are never even scored; the optional kind filter is applied in that
// same pass. See Store.Search.
func (s *InMemoryStore) Search(_ context.Context, sc Scope, query []float32, limit int, kinds ...Kind) ([]ScoredPoint, error) {
	if len(query) != s.dim {
		return nil, ErrDimensionMismatch
	}
	if limit <= 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []ScoredPoint
	for _, p := range s.points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			continue // isolation filter during the scan, not after
		}
		if !kindAllowed(kinds, p.Kind) {
			continue
		}
		out = append(out, ScoredPoint{Point: p, Score: cosine(query, p.Vector)})
	}
	// Highest similarity first; break ties by ID for a deterministic order
	// (map iteration is randomized).
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

// Delete removes the given ids from the index, but only when the stored
// point's (tenant,user) matches sc — checked before every delete, so a
// colliding id belonging to a different tenant/user is left untouched even
// though InMemoryStore's map is keyed by id alone. Unknown ids are ignored.
// See Store.Delete.
func (s *InMemoryStore) Delete(_ context.Context, sc Scope, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		p, ok := s.points[id]
		if !ok {
			continue // unknown id: no-op
		}
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			continue // scope mismatch: never delete another tenant's point
		}
		delete(s.points, id)
	}
	return nil
}

// DeleteBySource removes every point within sc whose Kind and Source match,
// checked before every delete exactly like Delete, so a colliding
// (kind,source) belonging to a different tenant/user is left untouched. See
// Store.DeleteBySource.
func (s *InMemoryStore) DeleteBySource(_ context.Context, sc Scope, kind Kind, source string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for id, p := range s.points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			continue // isolation filter during the scan, not after
		}
		if p.Kind != kind || p.Source != source {
			continue
		}
		delete(s.points, id)
		n++
	}
	return n, nil
}

// PruneTurns evicts KindTurn points within sc by age and/or count cap. See
// Store.PruneTurns. Only KindTurn points are ever scanned/deleted — Document
// and Fact points never enter toDelete. Scoped to sc exactly like Delete: the
// scan itself is filtered to sc's (tenant,user), so another tenant/user's
// KindTurn points are never even considered.
func (s *InMemoryStore) PruneTurns(_ context.Context, sc Scope, maxAge time.Duration, maxCount int) (int, error) {
	if maxAge <= 0 && maxCount <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var turns []Point
	for _, p := range s.points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() || p.Kind != KindTurn {
			continue
		}
		turns = append(turns, p)
	}

	toDelete := make(map[string]struct{})

	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		for _, p := range turns {
			if p.CreatedAt.Before(cutoff) {
				toDelete[p.ID] = struct{}{}
			}
		}
	}

	if maxCount > 0 && len(turns) > maxCount {
		// Newest first, so the surviving prefix is the maxCount most recent.
		sort.Slice(turns, func(i, j int) bool {
			if !turns[i].CreatedAt.Equal(turns[j].CreatedAt) {
				return turns[i].CreatedAt.After(turns[j].CreatedAt)
			}
			return turns[i].ID < turns[j].ID // deterministic tiebreak
		})
		for _, p := range turns[maxCount:] {
			toDelete[p.ID] = struct{}{}
		}
	}

	for id := range toDelete {
		delete(s.points, id)
	}
	return len(toDelete), nil
}

// EraseScope deletes every point for sc's (tenant,user) across all kinds — the
// semantic half of subject-level erasure (GDPR Art.17). The (tenant,user) check
// is applied during the scan exactly like Delete, so another tenant/user's
// points are never removed; tenant-shared KindDocument points (empty UserID)
// never match a user-scoped sc and are left intact. Returns the number removed.
// See ScopeEraser.
func (s *InMemoryStore) EraseScope(_ context.Context, sc Scope) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for id, p := range s.points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			continue // isolation filter during the scan, not after
		}
		delete(s.points, id)
		n++
	}
	return n, nil
}

// Close is a no-op for the in-memory index.
func (s *InMemoryStore) Close() error { return nil }

// cosine returns the cosine similarity of two equal-length vectors in [-1,1],
// or 0 if either is a zero vector. Accumulates in float64 for stability.
func cosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
