package semantic

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qdrant/go-client/qdrant"
)

// QdrantStore is the networked semantic store: the same Store contract as
// InMemoryStore, backed by Qdrant's HNSW index over gRPC (ADR-0001). Qdrant is
// the one service in the stack that scales independently of the Go binary
// (docs/components/qdrant-semantic-memory.md).
//
// Multi-tenancy uses the shared-collection + payload-filter model: a single
// collection holds every tenant's vectors, and each Search carries a hard
// (tenant_id, user_id) filter evaluated DURING the HNSW traversal. tenant_id
// and user_id are keyword-indexed so that filter is fast and single-pass —
// never a post-filter, which would be a tenant leak.
type QdrantStore struct {
	client     *qdrant.Client
	collection string
	dim        int
}

// QdrantConfig configures a QdrantStore connection.
type QdrantConfig struct {
	Host       string // default "localhost"
	Port       int    // default 6334 (gRPC)
	APIKey     string
	UseTLS     bool
	Collection string // default "aisotropy_knowledge"
	Dim        int    // vector dimension; must match the Embedder
}

// OpenQdrant connects to Qdrant, ensures the collection exists with cosine
// distance and keyword payload indexes on tenant_id/user_id, and returns a
// ready store.
func OpenQdrant(ctx context.Context, cfg QdrantConfig) (*QdrantStore, error) {
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.Port == 0 {
		cfg.Port = 6334
	}
	if cfg.Collection == "" {
		cfg.Collection = "aisotropy_knowledge"
	}
	if cfg.Dim <= 0 {
		return nil, errors.New("semantic: qdrant requires a positive vector dimension")
	}

	client, err := qdrant.NewClient(&qdrant.Config{
		Host:   cfg.Host,
		Port:   cfg.Port,
		APIKey: cfg.APIKey,
		UseTLS: cfg.UseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: connect qdrant %s:%d: %w", cfg.Host, cfg.Port, err)
	}

	s := &QdrantStore{client: client, collection: cfg.Collection, dim: cfg.Dim}
	if err := s.ensureCollection(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return s, nil
}

// ensureCollection creates the collection and its payload indexes if absent.
// Idempotent: safe to call on every start.
func (s *QdrantStore) ensureCollection(ctx context.Context) error {
	exists, err := s.client.CollectionExists(ctx, s.collection)
	if err != nil {
		return fmt.Errorf("semantic: qdrant collection exists check: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: s.collection,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(s.dim),
			Distance: qdrant.Distance_Cosine,
		}),
	}); err != nil {
		return fmt.Errorf("semantic: qdrant create collection: %w", err)
	}
	// Keyword indexes make the isolation filter a fast, single-pass condition
	// during the HNSW scan rather than a brute post-filter.
	for _, field := range []string{payloadTenant, payloadUser} {
		if _, err := s.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: s.collection,
			FieldName:      field,
			FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
		}); err != nil {
			return fmt.Errorf("semantic: qdrant index %q: %w", field, err)
		}
	}
	return nil
}

const (
	payloadTenant    = "tenant_id"
	payloadUser      = "user_id"
	payloadContent   = "content"
	payloadSource    = "source"
	payloadCitation  = "citation"
	payloadKind      = "kind"
	payloadCreatedAt = "created_at"
	payloadChunkIdx  = "chunk_index"
)

// scrollLimit bounds how many points a single filtered scroll pass returns,
// used by both PruneTurns (per scope) and DeleteBySource (per document).
// PruneTurns runs periodically (the sleep cycle) and is itself what keeps a
// scope's Turn population bounded, so this ceiling is a defensive cap on one
// pass's own cost, not the steady-state population; likewise a single
// document's chunk count is expected to stay well under this ceiling.
const scrollLimit = 10000

// Upsert writes points, keyed by their UUIDv5 ID (a re-upsert replaces in
// place, so an at-least-once retry is a no-op). See Store.Upsert.
func (s *QdrantStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	ps := make([]*qdrant.PointStruct, 0, len(points))
	for _, p := range points {
		if p.TenantID != sc.Tenant() || p.UserID != sc.User() {
			return ErrScopeMismatch
		}
		if len(p.Vector) != s.dim {
			return ErrDimensionMismatch
		}
		createdAt := p.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		ps = append(ps, &qdrant.PointStruct{
			Id:      qdrant.NewIDUUID(p.ID),
			Vectors: qdrant.NewVectors(p.Vector...),
			Payload: map[string]*qdrant.Value{
				payloadTenant:    qdrant.NewValueString(p.TenantID),
				payloadUser:      qdrant.NewValueString(p.UserID),
				payloadContent:   qdrant.NewValueString(p.Content),
				payloadSource:    qdrant.NewValueString(p.Source),
				payloadCitation:  qdrant.NewValueString(p.Citation),
				payloadKind:      qdrant.NewValueString(string(p.Kind)),
				payloadCreatedAt: qdrant.NewValueInt(createdAt.UnixMilli()),
				payloadChunkIdx:  qdrant.NewValueInt(int64(p.ChunkIndex)),
			},
		})
	}
	wait := true
	if _, err := s.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: s.collection,
		Points:         ps,
		Wait:           &wait,
	}); err != nil {
		return fmt.Errorf("semantic: qdrant upsert: %w", err)
	}
	return nil
}

// Search runs an ANN query filtered to (tenant,user). The filter is a must
// condition evaluated during the scan — another tenant's vectors are
// structurally unreachable. When kinds is non-empty, an additional must
// condition restricts the scan to points whose kind payload matches one of
// kinds (a match-any, evaluated in the same filtered scan, not a
// post-filter). See Store.Search.
func (s *QdrantStore) Search(ctx context.Context, sc Scope, query []float32, limit int, kinds ...Kind) ([]ScoredPoint, error) {
	if len(query) != s.dim {
		return nil, ErrDimensionMismatch
	}
	if limit <= 0 {
		return nil, nil
	}
	lim := uint64(limit)
	must := []*qdrant.Condition{
		qdrant.NewMatch(payloadTenant, sc.Tenant()),
		qdrant.NewMatch(payloadUser, sc.User()),
	}
	if len(kinds) > 0 {
		keywords := make([]string, len(kinds))
		for i, k := range kinds {
			keywords[i] = string(k)
		}
		must = append(must, qdrant.NewMatchKeywords(payloadKind, keywords...))
	}
	res, err := s.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collection,
		Query:          qdrant.NewQuery(query...),
		Filter:         &qdrant.Filter{Must: must},
		Limit:          &lim,
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: qdrant query: %w", err)
	}

	out := make([]ScoredPoint, 0, len(res))
	for _, r := range res {
		payload := r.GetPayload()
		out = append(out, ScoredPoint{
			Point: Point{
				ID:         r.GetId().GetUuid(),
				TenantID:   payload[payloadTenant].GetStringValue(),
				UserID:     payload[payloadUser].GetStringValue(),
				Kind:       Kind(payload[payloadKind].GetStringValue()),
				Content:    payload[payloadContent].GetStringValue(),
				Source:     payload[payloadSource].GetStringValue(),
				Citation:   payload[payloadCitation].GetStringValue(),
				CreatedAt:  time.UnixMilli(payload[payloadCreatedAt].GetIntegerValue()).UTC(),
				ChunkIndex: int(payload[payloadChunkIdx].GetIntegerValue()),
			},
			Score: r.GetScore(),
		})
	}
	return out, nil
}

// Delete removes the points identified by ids, scoped to sc. Deletion selects
// by a filter — has_id(ids) AND tenant_id AND user_id — rather than a bare id
// selector, so the isolation filter applies during the same delete operation:
// an id that collides with a different tenant/user's point is structurally
// unreachable by this call, mirroring Search's isolation guarantee. Ids that
// do not match (unknown, or belonging to a different scope) are silently
// unaffected. See Store.Delete.
func (s *QdrantStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	pointIDs := make([]*qdrant.PointId, len(ids))
	for i, id := range ids {
		pointIDs[i] = qdrant.NewIDUUID(id)
	}
	wait := true
	if _, err := s.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: s.collection,
		Points: qdrant.NewPointsSelectorFilter(&qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewHasID(pointIDs...),
				qdrant.NewMatch(payloadTenant, sc.Tenant()),
				qdrant.NewMatch(payloadUser, sc.User()),
			},
		}),
		Wait: &wait,
	}); err != nil {
		return fmt.Errorf("semantic: qdrant delete: %w", err)
	}
	return nil
}

// DeleteBySource removes every point within sc whose kind and source payload
// match, via a filtered scroll (to find candidate ids) followed by the same
// scoped Delete path used elsewhere in this file, so the isolation guarantee
// is identical to Delete's. See Store.DeleteBySource.
//
// Best-effort at scale: a single document's chunk count is expected to stay
// well under scrollLimit; a source with more matching points than that
// ceiling has only the first scrollLimit removed by one call.
func (s *QdrantStore) DeleteBySource(ctx context.Context, sc Scope, kind Kind, source string) (int, error) {
	must := []*qdrant.Condition{
		qdrant.NewMatch(payloadTenant, sc.Tenant()),
		qdrant.NewMatch(payloadUser, sc.User()),
		qdrant.NewMatchKeyword(payloadKind, string(kind)),
		qdrant.NewMatch(payloadSource, source),
	}
	ids, err := s.scrollIDs(ctx, must, nil)
	if err != nil {
		return 0, fmt.Errorf("semantic: qdrant delete by source scroll: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.Delete(ctx, sc, ids); err != nil {
		return 0, fmt.Errorf("semantic: qdrant delete by source: %w", err)
	}
	return len(ids), nil
}

// PruneTurns evicts KindTurn points within sc by age and/or count cap. See
// Store.PruneTurns. Both criteria are evaluated via a payload-filtered scroll
// (never touching Document/Fact points, whose kind never matches the
// KindTurn condition below) and the union of the two matched id sets is then
// removed through the same scoped Delete path used elsewhere in this file,
// so the isolation guarantee is identical.
//
// The count-cap criterion is best-effort: it orders by created_at (desc) and
// scrolls up to scrollLimit points, so a scope whose Turn population
// exceeds that ceiling in a single pass keeps only the ceiling's worth
// pruned this pass rather than the full excess — steady-state periodic
// pruning converges it down regardless.
func (s *QdrantStore) PruneTurns(ctx context.Context, sc Scope, maxAge time.Duration, maxCount int) (int, error) {
	if maxAge <= 0 && maxCount <= 0 {
		return 0, nil
	}
	base := []*qdrant.Condition{
		qdrant.NewMatch(payloadTenant, sc.Tenant()),
		qdrant.NewMatch(payloadUser, sc.User()),
		qdrant.NewMatchKeyword(payloadKind, string(KindTurn)),
	}

	toDelete := make(map[string]struct{})

	if maxAge > 0 {
		cutoff := float64(time.Now().Add(-maxAge).UnixMilli())
		must := append(append([]*qdrant.Condition{}, base...), qdrant.NewRange(payloadCreatedAt, &qdrant.Range{Lt: &cutoff}))
		ids, err := s.scrollIDs(ctx, must, nil)
		if err != nil {
			return 0, fmt.Errorf("semantic: qdrant prune turns by age: %w", err)
		}
		for _, id := range ids {
			toDelete[id] = struct{}{}
		}
	}

	if maxCount > 0 {
		desc := qdrant.Direction_Desc
		order := &qdrant.OrderBy{Key: payloadCreatedAt, Direction: &desc}
		ids, err := s.scrollIDs(ctx, base, order)
		if err != nil {
			return 0, fmt.Errorf("semantic: qdrant prune turns by count: %w", err)
		}
		if len(ids) > maxCount {
			for _, id := range ids[maxCount:] {
				toDelete[id] = struct{}{}
			}
		}
	}

	if len(toDelete) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(toDelete))
	for id := range toDelete {
		ids = append(ids, id)
	}
	if err := s.Delete(ctx, sc, ids); err != nil {
		return 0, fmt.Errorf("semantic: qdrant prune turns delete: %w", err)
	}
	return len(ids), nil
}

// EraseScope deletes every point for sc's (tenant,user) across ALL kinds — the
// semantic half of subject-level erasure (GDPR Art.17). It repeatedly scrolls a
// batch of matching ids (filtered to tenant_id+user_id during the scan) and
// deletes them through the same scoped Delete path used elsewhere in this file,
// until a pass returns nothing — so erasure is complete even for a subject with
// more than scrollLimit points. Tenant-shared KindDocument points carry an
// empty user_id and never match a user-scoped filter, so they are left intact.
// Returns the total number of points removed. See semantic.ScopeEraser.
func (s *QdrantStore) EraseScope(ctx context.Context, sc Scope) (int, error) {
	must := []*qdrant.Condition{
		qdrant.NewMatch(payloadTenant, sc.Tenant()),
		qdrant.NewMatch(payloadUser, sc.User()),
	}
	var total int
	for {
		ids, err := s.scrollIDs(ctx, must, nil)
		if err != nil {
			return total, fmt.Errorf("semantic: qdrant erase scope scroll: %w", err)
		}
		if len(ids) == 0 {
			return total, nil
		}
		if err := s.Delete(ctx, sc, ids); err != nil {
			return total, fmt.Errorf("semantic: qdrant erase scope delete: %w", err)
		}
		total += len(ids)
		if len(ids) < scrollLimit {
			return total, nil
		}
	}
}

// scrollIDs returns up to scrollLimit point ids matching must, optionally
// ordered by order. Used by PruneTurns (candidate KindTurn ids for each of
// its two eviction criteria) and by DeleteBySource (candidate ids for one
// document's chunks).
func (s *QdrantStore) scrollIDs(ctx context.Context, must []*qdrant.Condition, order *qdrant.OrderBy) ([]string, error) {
	lim := uint32(scrollLimit)
	res, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: s.collection,
		Filter:         &qdrant.Filter{Must: must},
		Limit:          &lim,
		OrderBy:        order,
		WithPayload:    qdrant.NewWithPayload(false),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res))
	for _, r := range res {
		ids = append(ids, r.GetId().GetUuid())
	}
	return ids, nil
}

// Close closes the gRPC connection.
func (s *QdrantStore) Close() error { return s.client.Close() }
