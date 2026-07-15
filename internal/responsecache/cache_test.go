package responsecache

import (
	"context"
	"testing"
	"time"
)

func newFacade(t *testing.T, emb *fakeEmbedder, threshold float32) *ResponseCache {
	t.Helper()
	exact := NewExactCache(NewLRUStore(64), time.Minute, false)
	var sem *SemanticCache
	if emb != nil {
		sem = newSemanticCache(t, emb, threshold, time.Minute, 0)
	}
	return New(exact, sem)
}

func TestResponseCacheExactFirst(t *testing.T) {
	emb := &fakeEmbedder{dim: 3, vectors: map[string][]float32{"q": {1, 0, 0}}}
	rc := newFacade(t, emb, 0.9)
	scope := Scope{Tenant: "acme", User: "alice"}
	ctx := context.Background()

	if err := rc.Set(ctx, scope, "q", "answer"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := rc.Get(ctx, scope, "q")
	if err != nil || !ok || got != "answer" {
		t.Fatalf("expected exact hit 'answer', got ok=%v got=%q err=%v", ok, got, err)
	}
	if m := rc.Metrics(); m.ExactHits != 1 || m.SemanticHits != 0 {
		t.Fatalf("expected 1 exact hit, got %+v", m)
	}
}

func TestResponseCacheFallsThroughToSemantic(t *testing.T) {
	// "q1" and "q2" are near-identical vectors (cosine ~0.99) but distinct
	// strings, so the exact layer misses and the semantic layer hits.
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"q1": {1, 0, 0},
			"q2": {0.99, 0.141, 0},
		},
	}
	rc := newFacade(t, emb, 0.9)
	scope := Scope{Tenant: "acme", User: "alice"}
	ctx := context.Background()

	if err := rc.Set(ctx, scope, "q1", "shared-answer"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := rc.Get(ctx, scope, "q2")
	if err != nil || !ok || got != "shared-answer" {
		t.Fatalf("expected semantic fallthrough hit, got ok=%v got=%q err=%v", ok, got, err)
	}
	if m := rc.Metrics(); m.ExactHits != 0 || m.SemanticHits != 1 {
		t.Fatalf("expected 1 semantic hit, got %+v", m)
	}
}

func TestResponseCacheMissMetrics(t *testing.T) {
	emb := &fakeEmbedder{dim: 3, vectors: map[string][]float32{
		"stored": {1, 0, 0},
		"other":  {0, 1, 0},
	}}
	rc := newFacade(t, emb, 0.9)
	scope := Scope{Tenant: "acme", User: "alice"}
	ctx := context.Background()

	_ = rc.Set(ctx, scope, "stored", "v")
	if _, ok, _ := rc.Get(ctx, scope, "other"); ok {
		t.Fatalf("expected miss")
	}
	if m := rc.Metrics(); m.Misses != 1 || m.Hits() != 0 {
		t.Fatalf("expected 1 miss, got %+v", m)
	}
}

func TestResponseCacheExactOnly(t *testing.T) {
	rc := newFacade(t, nil, 0) // no semantic layer
	scope := Scope{Tenant: "acme", User: "alice"}
	ctx := context.Background()

	if err := rc.Set(ctx, scope, "q", "a"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, ok, _ := rc.Get(ctx, scope, "q"); !ok || got != "a" {
		t.Fatalf("exact-only cache should hit, got ok=%v got=%q", ok, got)
	}
	if _, ok, _ := rc.Get(ctx, scope, "unseen"); ok {
		t.Fatalf("exact-only cache should miss on unseen prompt")
	}
}

func TestNewPanicsWithoutExact(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected New(nil, ...) to panic")
		}
	}()
	New(nil, nil)
}

// TestResponseCacheCrossTenantIsolation is the top-level acceptance criterion:
// a response written under (tenantA, alice) must be invisible to Gets under any
// other scope, across BOTH the exact and semantic layers of the facade.
func TestResponseCacheCrossTenantIsolation(t *testing.T) {
	// The prompt embeds to a single shared vector so that, absent partitioning,
	// the semantic layer WOULD hit across scopes (cosine 1.0). Isolation must
	// therefore be structural, not distance-based.
	emb := &fakeEmbedder{
		dim: 3,
		vectors: map[string][]float32{
			"secret prompt": {1, 0, 0},
		},
	}
	rc := newFacade(t, emb, 0.5)
	ctx := context.Background()

	writer := Scope{Tenant: "tenantA", User: "alice"}
	if err := rc.Set(ctx, writer, "secret prompt", "A-alice-response"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Control: the writer's own scope hits (proving the entry exists).
	if got, ok, _ := rc.Get(ctx, writer, "secret prompt"); !ok || got != "A-alice-response" {
		t.Fatalf("control read under writer scope failed: ok=%v got=%q", ok, got)
	}

	// Every other scope must miss on BOTH the exact key and the semantic search.
	foreign := []Scope{
		{Tenant: "tenantB", User: "alice"},
		{Tenant: "tenantB", User: "bob"},
		{Tenant: "tenantA", User: "bob"},
		{Tenant: "tenantA", User: ""},
		{Tenant: "", User: "alice"},
	}
	for _, fs := range foreign {
		if got, ok, err := rc.Get(ctx, fs, "secret prompt"); ok || err != nil {
			t.Fatalf("CROSS-TENANT LEAK: scope %+v got %q (ok=%v err=%v) for entry written under %+v",
				fs, got, ok, err, writer)
		}
	}
}
