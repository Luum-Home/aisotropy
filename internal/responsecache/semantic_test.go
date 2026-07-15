package responsecache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeEmbedder is a deterministic, dependency-free embedder for tests. It
// returns a caller-configured vector per exact text; unknown texts fall back to
// a fixed one-hot vector derived from the text length so behavior stays
// deterministic. Set err to exercise the embedding-failure path.
type fakeEmbedder struct {
	dim     int
	vectors map[string][]float32
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := f.vectors[t]; ok {
			out[i] = v
			continue
		}
		v := make([]float32, f.dim)
		v[len(t)%f.dim] = 1
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) Dimension() int { return f.dim }

func newSemanticCache(t *testing.T, emb *fakeEmbedder, threshold float32, ttl time.Duration, maxPerScope int) *SemanticCache {
	t.Helper()
	sc, err := NewSemanticCache(emb, threshold, ttl, maxPerScope)
	if err != nil {
		t.Fatalf("NewSemanticCache: %v", err)
	}
	return sc
}

func TestNewSemanticCacheRequiresEmbedder(t *testing.T) {
	if _, err := NewSemanticCache(nil, 0.9, time.Minute, 0); !errors.Is(err, ErrNoEmbedder) {
		t.Fatalf("expected ErrNoEmbedder, got %v", err)
	}
}

func TestSemanticCacheHitAboveThreshold(t *testing.T) {
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"capital of France":     {1, 0, 0},
			"the capital of France": {0.99, 0.141, 0}, // cosine ~0.9899 with above
		},
	}
	sc := newSemanticCache(t, emb, 0.9, time.Minute, 0)
	scope := Scope{Tenant: "acme", User: "alice"}

	if err := sc.Set(context.Background(), scope, "capital of France", "Paris"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := sc.Get(context.Background(), scope, "the capital of France")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("expected semantic hit above threshold, got miss")
	}
	if got != "Paris" {
		t.Fatalf("got %q, want %q", got, "Paris")
	}
}

func TestSemanticCacheMissBelowThreshold(t *testing.T) {
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"capital of France": {1, 0, 0},
			"weather tomorrow":  {0, 1, 0}, // cosine 0 with above
		},
	}
	sc := newSemanticCache(t, emb, 0.9, time.Minute, 0)
	scope := Scope{Tenant: "acme", User: "alice"}

	if err := sc.Set(context.Background(), scope, "capital of France", "Paris"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, ok, err := sc.Get(context.Background(), scope, "weather tomorrow"); err != nil || ok {
		t.Fatalf("expected clean miss below threshold, ok=%v err=%v", ok, err)
	}
}

func TestSemanticCacheTTLExpiry(t *testing.T) {
	emb := &fakeEmbedder{
		dim:     3,
		vectors: map[string][]float32{"q": {1, 0, 0}},
	}
	sc := newSemanticCache(t, emb, 0.9, 30*time.Second, 0)
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	sc.now = clk.now
	scope := Scope{Tenant: "acme", User: "alice"}

	if err := sc.Set(context.Background(), scope, "q", "A"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	clk.advance(29 * time.Second)
	if _, ok, _ := sc.Get(context.Background(), scope, "q"); !ok {
		t.Fatalf("expected hit within TTL")
	}

	clk.advance(2 * time.Second)
	if _, ok, _ := sc.Get(context.Background(), scope, "q"); ok {
		t.Fatalf("expected miss after TTL expiry")
	}
}

func TestSemanticCacheEmbedErrorIsMiss(t *testing.T) {
	sentinel := errors.New("boom")
	emb := &fakeEmbedder{dim: 3, err: sentinel}
	sc := newSemanticCache(t, emb, 0.9, time.Minute, 0)
	scope := Scope{Tenant: "acme", User: "alice"}

	if _, ok, err := sc.Get(context.Background(), scope, "q"); ok || !errors.Is(err, sentinel) {
		t.Fatalf("expected (false, embedder error), got ok=%v err=%v", ok, err)
	}
}

func TestSemanticCacheMaxPerScopeEviction(t *testing.T) {
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"p1": {1, 0, 0},
			"p2": {1, 0, 0},
			"p3": {1, 0, 0},
		},
	}
	sc := newSemanticCache(t, emb, 0.99, time.Minute, 2) // bound = 2
	scope := Scope{Tenant: "acme", User: "alice"}
	ctx := context.Background()

	_ = sc.Set(ctx, scope, "p1", "r1")
	_ = sc.Set(ctx, scope, "p2", "r2")
	_ = sc.Set(ctx, scope, "p3", "r3") // evicts p1 (oldest)

	sc.mu.Lock()
	n := len(sc.partitions[scope.partition()])
	sc.mu.Unlock()
	if n != 2 {
		t.Fatalf("expected partition bounded to 2, got %d", n)
	}
}

// TestSemanticCacheNeverMatchesAcrossScope is an acceptance criterion: a vector
// written under (A, alice) must NEVER be returned to a Get under any other
// scope, even when the query vector is byte-for-byte identical (cosine 1.0).
// Isolation must come from the partition, not from vector distance.
func TestSemanticCacheNeverMatchesAcrossScope(t *testing.T) {
	// Both prompts embed to the SAME vector, so distance can never explain a
	// miss — only partitioning can.
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"shared prompt": {1, 0, 0},
		},
	}
	sc := newSemanticCache(t, emb, 0.5, time.Minute, 0)
	ctx := context.Background()

	writer := Scope{Tenant: "tenantA", User: "alice"}
	if err := sc.Set(ctx, writer, "shared prompt", "A-alice-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A same-scope read is the control: it MUST hit (cosine 1.0, same partition).
	if got, ok, _ := sc.Get(ctx, writer, "shared prompt"); !ok || got != "A-alice-secret" {
		t.Fatalf("control same-scope read failed: ok=%v got=%q", ok, got)
	}

	// Every foreign scope MUST miss despite the identical vector.
	foreign := []Scope{
		{Tenant: "tenantB", User: "alice"}, // different tenant, same user
		{Tenant: "tenantB", User: "bob"},   // different tenant and user
		{Tenant: "tenantA", User: "bob"},   // same tenant, different user
		{Tenant: "tenantA", User: ""},      // same tenant, empty user
		{Tenant: "", User: "alice"},        // empty tenant
	}
	for _, fs := range foreign {
		got, ok, err := sc.Get(ctx, fs, "shared prompt")
		if err != nil {
			t.Fatalf("Get under %+v errored: %v", fs, err)
		}
		if ok {
			t.Fatalf("CROSS-SCOPE LEAK: scope %+v received %q written under %+v", fs, got, writer)
		}
	}
}

// TestSemanticPartitionKeyIsTenantUser asserts in code that the semantic
// partition map is keyed exactly by (tenant, user) — the structural basis of
// isolation. Two scopes with the same (tenant, user) share a partition; any
// difference in either field is a different partition.
func TestSemanticPartitionKeyIsTenantUser(t *testing.T) {
	a := Scope{Tenant: "t", User: "u"}
	b := Scope{Tenant: "t", User: "u"}
	if a.partition() != b.partition() {
		t.Fatalf("identical (tenant,user) must share a partition key")
	}
	diffs := []Scope{
		{Tenant: "t2", User: "u"},
		{Tenant: "t", User: "u2"},
	}
	for _, d := range diffs {
		if a.partition() == d.partition() {
			t.Fatalf("scope %+v must not share partition key with %+v", d, a)
		}
	}
	// The key must carry ONLY tenant and user.
	if (partitionKey{tenant: "t", user: "u"}) != a.partition() {
		t.Fatalf("partition key must be exactly {tenant,user}")
	}
}
