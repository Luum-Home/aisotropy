// Package responsecache is the Aisotropy inference/response cache — the
// "stage 4" lever the book describes: before an expensive LLM call, check
// whether an equivalent prompt has already been answered and, if so, serve the
// stored answer instead of re-invoking the model. Two complementary layers:
//
//   - Exact cache: a hash of the normalized prompt. O(1), zero embedding cost,
//     matches only byte-for-byte-after-normalization repeats. This is the cheap
//     first line of defense (see exact.go).
//   - Semantic cache: an embedding + nearest-neighbor lookup that also catches
//     paraphrases and reworded-but-equivalent prompts, gated by a cosine
//     similarity threshold and a TTL (see semantic.go).
//
// ResponseCache (cache.go) is the facade: exact-first, then semantic, with
// hit/miss metrics.
//
// # Isolation is the #1 correctness property, by construction
//
// A cache entry is ALWAYS keyed by (tenant, user) in addition to the prompt. A
// Get under scope A can NEVER return an entry written under scope B — a
// cross-tenant cache hit would be a catastrophic data leak (one tenant reading
// another tenant's model output), the exact isolation the whole runtime
// enforces at every storage tier. This is not a post-filter: the exact key
// mixes tenant\0user\0prompt into the SHA-256, and the semantic layer confines
// the nearest-neighbor search to a per-(tenant,user) partition so vectors from
// another scope are structurally unreachable — never scored, never compared.
// Treat the scope like the storage tiers do: in the key / in the partition, no
// unscoped read. TestResponseCacheCrossTenantIsolation and
// TestSemanticCacheNeverMatchesAcrossScope are the acceptance criteria.
//
// # Relationship to provider-level prompt caching
//
// This is distinct from, and complementary to, provider-level prompt caching
// (for example Anthropic's prompt caching / cache_control breakpoints, or
// OpenAI's automatic prompt caching). Those operate INSIDE a single model call:
// the provider caches the prefix of a request (system prompt, tool definitions,
// long context) so re-sending the same prefix is billed and processed more
// cheaply, but the model is still invoked and still generates fresh output
// tokens. This package operates OUTSIDE the model call: on a hit, no request is
// sent to the provider at all — the stored response is returned directly, so it
// also removes latency and output-token cost entirely. The two stack: a request
// that misses this cache can still benefit from provider prefix caching on the
// resulting call. This package intentionally does not depend on any provider's
// caching feature; it is provider-agnostic and works with the keyless stub LLM
// client just as well as with a real one.
//
// # Wiring
//
// This package is standalone and is NOT wired into the request path by itself;
// a caller (for example the chat handler) constructs a ResponseCache, checks it
// before the LLM call, and populates it after a successful generation. The
// Scope passed to Get/Set MUST be derived from the request's validated identity
// (the tenant_id/user_id path params that internal/tenancy validates), never
// from the request body.
package responsecache
