package responsecache

import (
	"context"
	"sync/atomic"
)

// Metrics is a snapshot of a ResponseCache's counters.
type Metrics struct {
	// ExactHits is the number of Gets served from the exact cache.
	ExactHits uint64
	// SemanticHits is the number of Gets served from the semantic cache (after
	// an exact miss).
	SemanticHits uint64
	// Misses is the number of Gets that neither layer could serve.
	Misses uint64
	// Errors is the number of Gets where the semantic layer's embedder errored;
	// these are also counted as Misses (the caller falls through to the model).
	Errors uint64
}

// Hits is the total number of served responses (exact + semantic).
func (m Metrics) Hits() uint64 { return m.ExactHits + m.SemanticHits }

// ResponseCache is the two-layer inference cache facade: exact-first, then
// semantic. It is the stage-4 lever a caller consults before an LLM call and
// populates after a successful generation.
//
// Every Get and Set is scoped by (tenant, user); both layers key/partition by
// that scope, so no cross-tenant hit is possible (see the package doc and the
// isolation tests). The semantic layer is optional: a ResponseCache built
// without one is a pure exact cache.
type ResponseCache struct {
	exact    *ExactCache
	semantic *SemanticCache // may be nil

	exactHits    atomic.Uint64
	semanticHits atomic.Uint64
	misses       atomic.Uint64
	errors       atomic.Uint64
}

// New constructs a ResponseCache from an exact cache (required) and an optional
// semantic cache (nil for exact-only). It panics if exact is nil, since a
// response cache with no layers is a programming error, not a runtime
// condition.
func New(exact *ExactCache, semantic *SemanticCache) *ResponseCache {
	if exact == nil {
		panic("responsecache: New requires a non-nil exact cache")
	}
	return &ResponseCache{exact: exact, semantic: semantic}
}

// Get looks up a cached response for (scope, prompt): exact cache first, then
// the semantic cache (if configured). It returns the response and true on a
// hit; ("", false, nil) on a clean miss; and ("", false, err) if the semantic
// layer's embedder errored — in the error case the caller should proceed to the
// model exactly as on a miss (the miss is also counted). Isolation is enforced
// by both layers: the scope is baked into the exact key and confines the
// semantic search to its own (tenant, user) partition.
func (c *ResponseCache) Get(ctx context.Context, scope Scope, prompt string) (string, bool, error) {
	if response, ok := c.exact.Get(scope, prompt); ok {
		c.exactHits.Add(1)
		return response, true, nil
	}

	if c.semantic != nil {
		response, ok, err := c.semantic.Get(ctx, scope, prompt)
		if err != nil {
			c.errors.Add(1)
			c.misses.Add(1)
			return "", false, err
		}
		if ok {
			c.semanticHits.Add(1)
			return response, true, nil
		}
	}

	c.misses.Add(1)
	return "", false, nil
}

// Set stores response for (scope, prompt) in both layers (the semantic layer
// only if configured). A semantic-layer embedding error is returned but the
// exact entry is still written — a partial store is preferable to none, and the
// exact layer is the cheaper, higher-confidence one. The error lets the caller
// log/observe the semantic-layer failure.
func (c *ResponseCache) Set(ctx context.Context, scope Scope, prompt, response string) error {
	c.exact.Set(scope, prompt, response)
	if c.semantic != nil {
		return c.semantic.Set(ctx, scope, prompt, response)
	}
	return nil
}

// Metrics returns a snapshot of the cache counters.
func (c *ResponseCache) Metrics() Metrics {
	return Metrics{
		ExactHits:    c.exactHits.Load(),
		SemanticHits: c.semanticHits.Load(),
		Misses:       c.misses.Load(),
		Errors:       c.errors.Load(),
	}
}
