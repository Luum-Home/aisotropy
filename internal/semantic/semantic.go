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
	"time"

	"github.com/google/uuid"

	"github.com/Luum-Home/aisotropy/internal/tenancy"
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
//
// Obtain a Scope through NewScope or ScopeFromContext, never a bare
// semantic.Scope{...} literal: its fields are unexported, so a bare literal
// outside this package does not even compile. Both constructors route
// identity through internal/tenancy (ADR-0011), so tenant/user are validated
// by the single shared rule (memory.ValidateIdentityComponent) that the
// working-memory, facts, and tenancy tiers also use, and the tiers can never
// drift on what a valid identity is.
type Scope struct {
	tenant string
	user   string
}

// Tenant returns the scope's tenant identity.
func (s Scope) Tenant() string { return s.tenant }

// User returns the scope's user identity.
func (s Scope) User() string { return s.user }

// NewScope validates tenant/user through internal/tenancy and returns a
// semantic Scope, or an error if either component is empty or malformed. It
// is the blessed constructor for production call sites: identity is
// validated by the same path internal/tenancy and internal/memory use,
// satisfying ADR-0011's "reconciliation with the facts tier" for the
// semantic tier too, without duplicating the validation rule here.
func NewScope(tenant, user string) (Scope, error) {
	ctx := tenancy.WithUser(tenancy.WithTenant(context.Background(), tenant), user)
	return ScopeFromContext(ctx)
}

// ScopeFromContext derives a semantic Scope from identity already wired into
// ctx via tenancy.WithTenant/WithUser, validating through internal/tenancy.
// Use it when a validated identity is already carried on the request ctx.
func ScopeFromContext(ctx context.Context) (Scope, error) {
	ts, err := tenancy.FromContext(ctx)
	if err != nil {
		return Scope{}, err
	}
	user, _ := ts.User()
	return Scope{tenant: ts.Tenant(), user: user}, nil
}

// Kind classifies what a Point represents, per ADR-0017
// (docs/decisions/0017-semantic-tier-multi-kind-memory.md): the semantic tier
// is a multi-kind associative memory, and each kind carries its own
// lifecycle, bound, and retrieval weight. Every Point MUST carry a Kind — see
// the ADR's "Mandatory going forward."
type Kind string

const (
	// KindDocument is curated tenant knowledge ingested through a dedicated
	// path (a follow-up feature this model enables). Persists until explicitly
	// removed; unbounded; high retrieval weight.
	KindDocument Kind = "document"
	// KindFact is a distilled fact's own content, dual-indexed and keyed by
	// the fact id. Its lifecycle is tied to the fact: superseding or deleting
	// the fact deletes its vector too (owned by the MemoryStore facade, a
	// later phase — ADR-0017 phase 2). High retrieval weight.
	KindFact Kind = "fact"
	// KindTurn is a raw conversational turn embedded by consolidation.
	// Ephemeral: bounded by a TTL/per-(tenant,user) cap and evicted by the
	// sleep cycle (a later phase — ADR-0017 phase 3). Low retrieval weight
	// (fallback only).
	KindTurn Kind = "turn"
)

// Point is one embedded chunk of tenant knowledge. Content is the payload
// text; Vector is its embedding.
type Point struct {
	// ID is the deterministic UUIDv5 idempotency key (see PointID). Re-upserting
	// the same ID replaces in place, so an at-least-once Outbox worker (Phase 5)
	// can retry without duplicating vectors.
	ID       string
	TenantID string
	UserID   string
	// Kind classifies this point per ADR-0017; see the Kind type doc.
	Kind Kind
	// Content is the chunk text kept as payload so retrieval returns usable
	// text (and the reranker can read it), not just an id.
	Content string
	// Source is optional provenance (document id, url, section) for citations.
	Source string
	// Vector is the embedding; its length must equal the store's dimension.
	Vector []float32
	// CreatedAt is when this point was written, set by the writer at upsert
	// time (consolidation for Turn-kind, the engram facade for Fact-kind).
	// It is the basis of PruneTurns' age-TTL eviction (ADR-0017 phase 3):
	// implementations persist it (Qdrant as a payload field, mirroring the
	// facts tier's unix-millis timestamp convention) so it survives restarts.
	CreatedAt time.Time
}

// ScoredPoint is a Point returned from Search, annotated with its cosine
// similarity to the query (higher = closer).
type ScoredPoint struct {
	Point
	Score float32
}

// Store is the semantic-memory engine. Implementations MUST filter every
// Search and Delete by (tenant_id, user_id) and MUST make Upsert idempotent
// on Point.ID.
type Store interface {
	// Upsert inserts or replaces points by ID. Every point's TenantID/UserID
	// must match sc, else ErrScopeMismatch. Vectors must match the store
	// dimension, else ErrDimensionMismatch.
	Upsert(ctx context.Context, sc Scope, points []Point) error

	// Search returns up to limit points for sc most similar to query, ranked by
	// cosine similarity, highest first. The (tenant,user) filter is applied
	// during the scan; another tenant's vectors are structurally unreachable.
	// When kinds is non-empty, only points whose Kind is one of kinds are
	// scored/returned — applied in the same scan as the isolation filter, not
	// as a post-filter. An empty kinds (the zero-arg call) means "all kinds",
	// preserving the pre-ADR-0017 behavior for existing callers.
	Search(ctx context.Context, sc Scope, query []float32, limit int, kinds ...Kind) ([]ScoredPoint, error)

	// Delete removes the points identified by ids, scoped to sc: a point is
	// only removed if it belongs to sc's (tenant,user) — applied during the
	// same lookup, never as a post-filter — so a colliding id belonging to a
	// different tenant/user is left untouched. Unknown ids are ignored (no
	// error).
	Delete(ctx context.Context, sc Scope, ids []string) error

	// PruneTurns evicts KindTurn points within sc per ADR-0017 phase 3
	// ("Turn-kind bounding"): every KindTurn point older than maxAge is
	// deleted (when maxAge > 0), and, independently, only the maxCount
	// most-recent (by CreatedAt) KindTurn points are kept, with the excess
	// deleted (when maxCount > 0) — the union of both criteria is removed.
	// maxAge <= 0 disables the age policy; maxCount <= 0 disables the count
	// policy; both <= 0 makes the call a no-op. Document- and Fact-kind
	// points are NEVER touched by this method, regardless of age or count.
	// Scoped to sc exactly like Delete: another tenant/user's KindTurn
	// points are structurally unreachable. Returns the number of points
	// deleted.
	PruneTurns(ctx context.Context, sc Scope, maxAge time.Duration, maxCount int) (int, error)

	// Close releases resources (index / gRPC client).
	Close() error
}

// kindAllowed reports whether k passes the kind filter kinds. An empty kinds
// (the common case: no filter requested) allows every kind.
func kindAllowed(kinds []Kind, k Kind) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
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
// chunk, from (tenant_id, user_id, kind, sourceID) per ADR-0017
// (docs/decisions/0017-semantic-tier-multi-kind-memory.md): encoding kind
// into the id means a Fact-kind vector and a Turn-kind (or Document-kind)
// vector derived from the same sourceID never collide, so each can be
// addressed and deleted independently. Reprocessing the same (kind, sourceID)
// yields the same id, so the upsert is a true no-op.
func PointID(sc Scope, kind Kind, sourceID string) string {
	name := sc.tenant + "\x00" + sc.user + "\x00" + string(kind) + "\x00" + sourceID
	return uuid.NewSHA1(pointNamespace, []byte(name)).String()
}
