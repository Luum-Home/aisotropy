package semantic

import (
	"context"
	"errors"
	"fmt"

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
	payloadTenant  = "tenant_id"
	payloadUser    = "user_id"
	payloadContent = "content"
	payloadSource  = "source"
)

// Upsert writes points, keyed by their UUIDv5 ID (a re-upsert replaces in
// place, so an at-least-once retry is a no-op). See Store.Upsert.
func (s *QdrantStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	ps := make([]*qdrant.PointStruct, 0, len(points))
	for _, p := range points {
		if p.TenantID != sc.TenantID || p.UserID != sc.UserID {
			return ErrScopeMismatch
		}
		if len(p.Vector) != s.dim {
			return ErrDimensionMismatch
		}
		ps = append(ps, &qdrant.PointStruct{
			Id:      qdrant.NewIDUUID(p.ID),
			Vectors: qdrant.NewVectors(p.Vector...),
			Payload: map[string]*qdrant.Value{
				payloadTenant:  qdrant.NewValueString(p.TenantID),
				payloadUser:    qdrant.NewValueString(p.UserID),
				payloadContent: qdrant.NewValueString(p.Content),
				payloadSource:  qdrant.NewValueString(p.Source),
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
// structurally unreachable. See Store.Search.
func (s *QdrantStore) Search(ctx context.Context, sc Scope, query []float32, limit int) ([]ScoredPoint, error) {
	if len(query) != s.dim {
		return nil, ErrDimensionMismatch
	}
	if limit <= 0 {
		return nil, nil
	}
	lim := uint64(limit)
	res, err := s.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collection,
		Query:          qdrant.NewQuery(query...),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch(payloadTenant, sc.TenantID),
				qdrant.NewMatch(payloadUser, sc.UserID),
			},
		},
		Limit:       &lim,
		WithPayload: qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: qdrant query: %w", err)
	}

	out := make([]ScoredPoint, 0, len(res))
	for _, r := range res {
		payload := r.GetPayload()
		out = append(out, ScoredPoint{
			Point: Point{
				ID:       r.GetId().GetUuid(),
				TenantID: payload[payloadTenant].GetStringValue(),
				UserID:   payload[payloadUser].GetStringValue(),
				Content:  payload[payloadContent].GetStringValue(),
				Source:   payload[payloadSource].GetStringValue(),
			},
			Score: r.GetScore(),
		})
	}
	return out, nil
}

// Close closes the gRPC connection.
func (s *QdrantStore) Close() error { return s.client.Close() }
