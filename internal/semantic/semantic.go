// Package semantic is the Aisotropy semantic-memory tier — the long-term
// associative memory of the LLM OS (docs/components/qdrant-semantic-memory.md).
// It stores embeddings of a tenant's bulk, unstructured knowledge (docs,
// manuals, knowledge bases) and resolves similarity search with HARD
// multi-tenant isolation: every query filters by (tenant_id, user_id), applied
// during the scan, never as a post-filter — a call site that omits the scope
// is a tenant leak (see the component doc's "poorly built filter = tenant
// leak" gotcha and .claude/rules/constitutional-gates.md).
//
// Following KB-over-RAG (docs/vision/principles.md), vectors are reserved for
// what cannot be curated by hand due to volume; one-off facts and preferences
// live in the facts tier (internal/facts, FTS5), not here.
//
// Two Store implementations share the same contract:
//   - InMemoryStore: brute-force cosine over an in-process index — the
//     zero-config local-first default and the substrate the tests exercise.
//   - QdrantStore: the same contract over Qdrant's gRPC API (ADR-0001), the
//     dedicated Rust vector engine and the one service that scales
//     independently of the Go binary.
package semantic

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrScopeMismatch is returned when a Point's TenantID/UserID does not match
// the Scope of the operation writing it — a cross-tenant write, which the
// isolation gate forbids.
var ErrScopeMismatch = errors.New("semantic: point tenant/user does not match the operation scope")

// ErrDimensionMismatch is returned when a vector's length does not match the
// store's/embedder's configured dimension.
var ErrDimensionMismatch = errors.New("semantic: vector dimension mismatch")

// pointNamespace is the fixed UUID namespace for deterministic v5 point ids.
// Stable across processes so the same (tenant,user,message) always maps to the
// same id — the basis of idempotent upserts. Do not change it: doing so would
// re-id every existing point.
var pointNamespace = uuid.MustParse("6f9b1f6e-2c1a-5e7d-9a3b-0a1b2c3d4e5f")

// Scope identifies the (tenant, user) a query is confined to, derived once at
// the Huma handler boundary from the validated path params (mirroring
// facts.Scope and memory.Key).
type Scope struct {
	TenantID string
	UserID   string
}

// Point is one embedded chunk of tenant knowledge. Content is the payload
// text; Vector is its embedding.
type Point struct {
	// ID is the deterministic UUIDv5 idempotency key (see PointID). Re-upserting
	// the same ID replaces in place, so an at-least-once Outbox worker (Phase 5)
	// can retry without duplicating vectors.
	ID       string
	TenantID string
	UserID   string
	// Content is the chunk text kept as payload so retrieval returns usable
	// text (and the reranker can read it), not just an id.
	Content string
	// Source is optional provenance (document id, url, section) for citations.
	Source string
	// Vector is the embedding; its length must equal the store's dimension.
	Vector []float32
}

// ScoredPoint is a Point returned from Search, annotated with its cosine
// similarity to the query (higher = closer).
type ScoredPoint struct {
	Point
	Score float32
}

// Store is the semantic-memory engine. Implementations MUST filter every
// Search by (tenant_id, user_id) and MUST make Upsert idempotent on Point.ID.
type Store interface {
	// Upsert inserts or replaces points by ID. Every point's TenantID/UserID
	// must match sc, else ErrScopeMismatch. Vectors must match the store
	// dimension, else ErrDimensionMismatch.
	Upsert(ctx context.Context, sc Scope, points []Point) error

	// Search returns up to limit points for sc most similar to query, ranked by
	// cosine similarity, highest first. The (tenant,user) filter is applied
	// during the scan; another tenant's vectors are structurally unreachable.
	Search(ctx context.Context, sc Scope, query []float32, limit int) ([]ScoredPoint, error)

	// Close releases resources (index / gRPC client).
	Close() error
}

// Embedder maps text to vectors. Phase 4 ships a deterministic HashEmbedder for
// local dev and tests; a real model (a local ONNX/gguf embedder or a provider
// API) is a drop-in behind this interface. All vectors from one Embedder share
// Dimension().
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
}

// Reranker reorders and trims ANN candidates before they enter the model's
// context, so only the few most relevant fragments are injected (the component
// doc's reranking step; ADR-0006 for the eventual cross-encoder choice).
type Reranker interface {
	// Rerank returns the topK most relevant candidates for query, reordered.
	// It never returns more than topK.
	Rerank(ctx context.Context, query string, candidates []ScoredPoint, topK int) []ScoredPoint
}

// PointID derives the deterministic UUIDv5 idempotency key for a knowledge
// chunk, from (tenant_id, user_id, messageID) exactly as
// docs/components/qdrant-semantic-memory.md specifies. Reprocessing the same
// message yields the same id, so the upsert is a true no-op.
func PointID(sc Scope, messageID string) string {
	name := sc.TenantID + "\x00" + sc.UserID + "\x00" + messageID
	return uuid.NewSHA1(pointNamespace, []byte(name)).String()
}
