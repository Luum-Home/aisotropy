package responsecache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// This file closes remaining statement-coverage gaps in the responsecache
// package's error-handling and edge-case branches not reached by
// cache_test.go / exact_test.go / semantic_test.go.

// --- cache.go: ResponseCache.Get's semantic-error branch --------------------

// TestResponseCacheGetSemanticErrorCountsMissAndError covers Get's
// "semantic layer errored" branch directly: on an exact miss, a semantic
// layer whose embedder fails must be treated as a miss (the caller falls
// through to the model) while ALSO being distinctly counted as an Errors
// event, not folded silently into an ordinary Misses.
func TestResponseCacheGetSemanticErrorCountsMissAndError(t *testing.T) {
	sentinel := errors.New("boom")
	emb := &fakeEmbedder{dim: 3, err: sentinel}
	rc := newFacade(t, emb, 0.9)
	scope := Scope{Tenant: "acme", User: "alice"}

	got, ok, err := rc.Get(context.Background(), scope, "never cached")
	if ok {
		t.Fatalf("expected a miss on semantic embedder error, got hit %q", got)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Get error = %v, want the embedder's sentinel error", err)
	}
	m := rc.Metrics()
	if m.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", m.Errors)
	}
	if m.Misses != 1 {
		t.Fatalf("Misses = %d, want 1 (an error also counts as a miss)", m.Misses)
	}
	if m.Hits() != 0 {
		t.Fatalf("Hits() = %d, want 0", m.Hits())
	}
}

// --- exact.go: NewLRUStore capacity clamp and update-in-place branch -------

// TestNewLRUStoreNonPositiveCapacityClampsToOne covers NewLRUStore's
// "capacity <= 0 => 1" guard: a zero-capacity cache is almost never intended,
// so it is treated as capacity 1 rather than storing nothing.
func TestNewLRUStoreNonPositiveCapacityClampsToOne(t *testing.T) {
	store := NewLRUStore(0)
	store.Set("a", "1", 0)
	store.Set("b", "2", 0) // must evict "a" (capacity clamped to 1)

	if _, ok := store.Get("a"); ok {
		t.Fatal("capacity-0 store retained more than 1 entry (want capacity clamped to 1)")
	}
	if v, ok := store.Get("b"); !ok || v != "2" {
		t.Fatalf("Get(b) = (%q,%v), want (\"2\",true)", v, ok)
	}
}

// TestLRUStoreSetUpdatesExistingKeyInPlace covers lruStore.Set's
// "key already present" branch: re-Setting an existing key must update its
// value/TTL and move it to the front WITHOUT growing the list (so it can
// never itself trigger eviction), rather than appending a duplicate node.
func TestLRUStoreSetUpdatesExistingKeyInPlace(t *testing.T) {
	store := NewLRUStore(2)
	store.Set("a", "first", 0)
	store.Set("b", "other", 0)
	store.Set("a", "updated", 0) // update-in-place, not a new entry

	if got := store.Len(); got != 2 {
		t.Fatalf("Len() after updating an existing key = %d, want 2 (no growth)", got)
	}
	if v, ok := store.Get("a"); !ok || v != "updated" {
		t.Fatalf("Get(a) = (%q,%v), want (\"updated\",true)", v, ok)
	}
	// "a" was just re-Set (and thus most-recently-used); adding a third distinct
	// key must evict "b" (the actual least-recently-used), never "a".
	store.Set("c", "third", 0)
	if _, ok := store.Get("b"); ok {
		t.Fatal("expected \"b\" (least-recently-used) to be evicted, but it is still present")
	}
	if _, ok := store.Get("a"); !ok {
		t.Fatal("expected \"a\" (just updated, most-recently-used) to survive eviction")
	}
}

// --- semantic.go: live() zero-TTL branch, Set's embed-error branch, embed's
// --- vector-count guard, cosineSimilarity's defensive branches -------------

// TestSemanticCacheZeroTTLNeverExpires covers live()'s "ttl <= 0 => always
// live" branch: with no configured TTL, an entry written long ago (per the
// injectable clock) must still be a hit, since none of the other existing
// tests configure ttl<=0 (they all pass a positive TTL).
func TestSemanticCacheZeroTTLNeverExpires(t *testing.T) {
	emb := &fakeEmbedder{dim: 3, vectors: map[string][]float32{"q": {1, 0, 0}}}
	sc := newSemanticCache(t, emb, 0.9, 0, 0) // ttl <= 0: never expires
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	sc.now = clk.now
	scope := Scope{Tenant: "acme", User: "alice"}

	if err := sc.Set(context.Background(), scope, "q", "A"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clk.advance(365 * 24 * time.Hour) // a year later
	if _, ok, err := sc.Get(context.Background(), scope, "q"); err != nil || !ok {
		t.Fatalf("Get with ttl<=0 after a year = (ok=%v err=%v), want a hit (never expires)", ok, err)
	}
}

// TestSemanticCacheSetEmbedErrorPropagates covers Set's embed-error branch
// directly (distinct from Get's, which TestSemanticCacheEmbedErrorIsMiss in
// semantic_test.go already exercises): a failing embedder must surface the
// error from Set and must NOT store a partial/zero-vector entry.
func TestSemanticCacheSetEmbedErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	emb := &fakeEmbedder{dim: 3, err: sentinel}
	sc := newSemanticCache(t, emb, 0.9, time.Minute, 0)
	scope := Scope{Tenant: "acme", User: "alice"}

	err := sc.Set(context.Background(), scope, "q", "unreachable")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Set with a failing embedder = %v, want the sentinel error", err)
	}
	if got := len(sc.partitions[scope.partition()]); got != 0 {
		t.Fatalf("partition has %d entries after a failed Set, want 0", got)
	}
}

// wrongCountEmbedder returns a mismatched number of vectors relative to the
// requested texts, exercising embed's own defensive count check rather than
// any real embedder's behavior.
type wrongCountEmbedder struct{ dim int }

func (e wrongCountEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{}, nil // zero vectors for a 1-text request
}
func (e wrongCountEmbedder) Dimension() int { return e.dim }

// TestSemanticCacheEmbedderVectorCountMismatch covers embed's
// "len(vecs) != 1" defensive branch: an embedder that returns the wrong
// number of vectors for a single-prompt request must produce a clear error,
// not an out-of-range panic on vecs[0].
func TestSemanticCacheEmbedderVectorCountMismatch(t *testing.T) {
	sc, err := NewSemanticCache(wrongCountEmbedder{dim: 3}, 0.9, time.Minute, 0)
	if err != nil {
		t.Fatalf("NewSemanticCache: %v", err)
	}
	scope := Scope{Tenant: "acme", User: "alice"}
	_, _, gerr := sc.Get(context.Background(), scope, "q")
	if gerr == nil {
		t.Fatal("Get with a vector-count-mismatched embedder = nil error, want an error")
	}
}

// TestCosineSimilarityDefensiveBranches covers cosineSimilarity's two
// defensive branches directly: mismatched vector lengths and a zero-magnitude
// vector both yield 0 (a miss) rather than a panic or NaN.
func TestCosineSimilarityDefensiveBranches(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("cosineSimilarity(mismatched lengths) = %v, want 0", got)
	}
	if got := cosineSimilarity([]float32{0, 0, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("cosineSimilarity(zero-magnitude a) = %v, want 0", got)
	}
	if got := cosineSimilarity([]float32{1, 0, 0}, []float32{0, 0, 0}); got != 0 {
		t.Fatalf("cosineSimilarity(zero-magnitude b) = %v, want 0", got)
	}
}
