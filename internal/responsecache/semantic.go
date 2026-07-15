package responsecache

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// ErrNoEmbedder is returned by NewSemanticCache when constructed without an
// embedder — a semantic cache with no way to embed prompts cannot function, and
// failing closed here is safer than a cache that silently never hits.
var ErrNoEmbedder = errors.New("responsecache: semantic cache requires a non-nil embedder")

// partitionKey is the map key for a semantic partition. It is derived ONLY from
// a Scope's tenant and user, so the partition a prompt's vector is stored in
// and searched against is fully determined by (tenant, user) — the by-
// construction guarantee that a Get under scope A can never reach vectors
// written under scope B. It is a distinct type (not Scope itself) so nothing
// other than the (tenant, user) pair can ever leak into the key.
type partitionKey struct {
	tenant string
	user   string
}

// partition returns the (tenant, user) partition key for s. This is the single
// point where a Scope becomes a partition key; keeping it in one place makes
// the isolation invariant auditable — grep for partition() and every access is
// visible.
func (s Scope) partition() partitionKey {
	return partitionKey{tenant: s.Tenant, user: s.User}
}

// semEntry is one cached (embedding, response) pair with its write time for TTL.
type semEntry struct {
	vector   []float32
	response string
	ts       time.Time
}

// SemanticCache is the nearest-neighbor response cache. On Get it embeds the
// prompt, searches ONLY the caller's (tenant, user) partition, and returns the
// stored response of the closest entry when its cosine similarity is >= the
// configured threshold and the entry is within TTL. It catches paraphrases the
// exact cache misses, at the cost of one embedding per lookup.
//
// Isolation is by construction: entries live in a map keyed by
// (tenant, user) (see partitionKey), and a lookup only ever scans its own
// scope's slice — another scope's vectors are never in the candidate set, so
// they are never scored or compared.
type SemanticCache struct {
	embedder  semantic.Embedder
	threshold float32
	ttl       time.Duration
	// maxPerScope bounds a single partition's size to keep memory and per-Get
	// scan cost bounded; oldest-first eviction. <= 0 disables the bound.
	maxPerScope int

	mu         sync.Mutex
	partitions map[partitionKey][]semEntry
	now        func() time.Time // injectable clock (tests set this)
}

// NewSemanticCache constructs a SemanticCache. embedder is required (else
// ErrNoEmbedder). threshold is the minimum cosine similarity for a hit, in
// [-1, 1]; a typical value is around 0.9. ttl <= 0 disables time-based expiry.
// maxPerScope <= 0 leaves each partition unbounded.
func NewSemanticCache(embedder semantic.Embedder, threshold float32, ttl time.Duration, maxPerScope int) (*SemanticCache, error) {
	if embedder == nil {
		return nil, ErrNoEmbedder
	}
	return &SemanticCache{
		embedder:    embedder,
		threshold:   threshold,
		ttl:         ttl,
		maxPerScope: maxPerScope,
		partitions:  make(map[partitionKey][]semEntry),
		now:         time.Now,
	}, nil
}

// live reports whether e is within ttl as of t. A zero/negative ttl means
// entries never expire by time.
func (c *SemanticCache) live(e semEntry, t time.Time) bool {
	if c.ttl <= 0 {
		return true
	}
	return t.Sub(e.ts) <= c.ttl
}

// Get embeds prompt and searches only scope's partition. It returns the closest
// entry's response and true when that entry is live and its similarity is >=
// threshold; otherwise ("", false, nil). A non-nil error means embedding
// failed — the caller should treat that as a miss and proceed to the model, not
// as a hit.
func (c *SemanticCache) Get(ctx context.Context, scope Scope, prompt string) (string, bool, error) {
	vec, err := c.embed(ctx, prompt)
	if err != nil {
		return "", false, err
	}

	key := scope.partition()

	c.mu.Lock()
	defer c.mu.Unlock()

	entries := c.partitions[key]
	if len(entries) == 0 {
		return "", false, nil
	}

	now := c.now()
	bestScore := float32(-2) // below the [-1,1] cosine range
	bestResponse := ""
	found := false
	for _, e := range entries {
		if !c.live(e, now) {
			continue
		}
		score := cosineSimilarity(vec, e.vector)
		if score > bestScore {
			bestScore = score
			bestResponse = e.response
			found = true
		}
	}

	if !found || bestScore < c.threshold {
		return "", false, nil
	}
	return bestResponse, true, nil
}

// Set embeds prompt and stores (embedding, response) in scope's partition,
// pruning expired entries and enforcing the per-scope size bound.
func (c *SemanticCache) Set(ctx context.Context, scope Scope, prompt, response string) error {
	vec, err := c.embed(ctx, prompt)
	if err != nil {
		return err
	}

	key := scope.partition()

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	entries := c.partitions[key]

	// Drop expired entries opportunistically so a partition does not accumulate
	// dead vectors between reads.
	if c.ttl > 0 {
		kept := entries[:0]
		for _, e := range entries {
			if c.live(e, now) {
				kept = append(kept, e)
			}
		}
		entries = kept
	}

	entries = append(entries, semEntry{vector: vec, response: response, ts: now})

	// Enforce the per-scope bound with oldest-first eviction. Entries are
	// appended in write order, so the front is oldest.
	if c.maxPerScope > 0 && len(entries) > c.maxPerScope {
		entries = entries[len(entries)-c.maxPerScope:]
	}

	c.partitions[key] = entries
	return nil
}

// embed runs the embedder for a single prompt and returns its vector, or an
// error if the embedder failed or returned no vector.
func (c *SemanticCache) embed(ctx context.Context, prompt string) ([]float32, error) {
	vecs, err := c.embedder.Embed(ctx, []string{prompt})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, errors.New("responsecache: embedder returned unexpected vector count")
	}
	return vecs[0], nil
}

// cosineSimilarity returns the cosine similarity of two vectors in [-1, 1], or
// 0 for a zero-length/zero-magnitude/mismatched-length input. Accumulates in
// float64 for numerical stability. Mismatched lengths yield 0 (a miss) rather
// than a panic — a defensive stance for a cache that must fail closed.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
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
