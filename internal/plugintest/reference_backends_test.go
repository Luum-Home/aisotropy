package plugintest

// Direct unit tests of the in-memory REFERENCE backends (RefVectorBackend /
// RefDocBackend). These are the correct, minimal implementations a community
// author reads and copies, so their own logic — cosine ranking, token-overlap
// relevance, k-truncation, cross-namespace Scan/ExportAll — is exercised
// directly here at the raw (tenant-unaware) namespace layer, independent of the
// sealed decorator the conformance suites drive them through.

import (
	"context"
	"math"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

func TestRefVectorBackend_SearchRanksTruncatesAndScans(t *testing.T) {
	ctx := context.Background()
	b := NewRefVectorBackend()

	// Two near-identical vectors and one different, under one namespace.
	near := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	far := []float32{0, 1, 0, 0, 0, 0, 0, 0}
	if err := b.Upsert(ctx, "ns1", []semantic.BackendVector{
		{ID: "a", Vector: near, Payload: map[string]string{"aisotropy.content": "alpha"}},
		{ID: "b", Vector: near, Payload: map[string]string{"aisotropy.content": "bravo"}},
		{ID: "c", Vector: far, Payload: map[string]string{"aisotropy.content": "charlie"}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Idempotent replace on ID: re-upsert "a" with new content, count stays 3.
	if err := b.Upsert(ctx, "ns1", []semantic.BackendVector{
		{ID: "a", Vector: near, Payload: map[string]string{"aisotropy.content": "alpha2"}},
	}); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}

	// k-truncation: ask for 2 of 3, best-first. The two "near" vectors outrank far.
	hits, err := b.Search(ctx, "ns1", near, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("k-truncation broken: got %d hits, want 2", len(hits))
	}
	if hits[0].ID != "a" && hits[0].ID != "b" {
		t.Fatalf("top hit should be a near vector, got %q", hits[0].ID)
	}
	// The idempotent replace is visible.
	for _, h := range hits {
		if h.ID == "a" && h.Payload["aisotropy.content"] != "alpha2" {
			t.Fatalf("idempotent replace not reflected: %q", h.Payload["aisotropy.content"])
		}
	}
	// k <= 0 returns everything (no truncation).
	all, err := b.Search(ctx, "ns1", near, 0)
	if err != nil {
		t.Fatalf("Search k=0: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("k=0 should return all 3, got %d", len(all))
	}

	// Search on an unknown namespace returns nothing (namespace isolation at the
	// raw layer).
	none, err := b.Search(ctx, "ns-unknown", near, 4)
	if err != nil {
		t.Fatalf("Search unknown ns: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown ns returned %d hits, want 0", len(none))
	}

	// Scan enumerates everything under a namespace.
	scanned, err := b.Scan(ctx, "ns1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(scanned) != 3 {
		t.Fatalf("Scan returned %d, want 3", len(scanned))
	}

	// Delete removes ids (unknown id ignored) and Namespaces reflects live buckets.
	if err := b.Delete(ctx, "ns1", []string{"a", "not-there"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, _ := b.Scan(ctx, "ns1")
	if len(after) != 2 {
		t.Fatalf("after Delete Scan = %d, want 2", len(after))
	}
	if ns := b.Namespaces(); len(ns) != 1 || ns[0] != "ns1" {
		t.Fatalf("Namespaces = %v, want [ns1]", ns)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRefVectorBackend_CosineEdgeCases(t *testing.T) {
	// Mismatched lengths yield 0 (guards a hostile/buggy caller).
	if got := cosine([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("cosine mismatched-length = %v, want 0", got)
	}
	// A zero vector yields 0 (no direction).
	if got := cosine([]float32{0, 0, 0}, []float32{1, 2, 3}); got != 0 {
		t.Fatalf("cosine zero-vector = %v, want 0", got)
	}
	// Identical direction yields ~1.
	if got := cosine([]float32{1, 1, 0}, []float32{2, 2, 0}); math.Abs(float64(got)-1) > 1e-6 {
		t.Fatalf("cosine identical-direction = %v, want ~1", got)
	}
}

func TestRefDocBackend_SearchScanExportAndGet(t *testing.T) {
	ctx := context.Background()
	b := NewRefDocBackend()

	// Two docs under nsA, one under nsB.
	if err := b.Put(ctx, "nsA", "k1", facts.BackendDoc{Key: "k1", Content: "the quick brown fox"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Put(ctx, "nsA", "k2", facts.BackendDoc{Key: "k2", Content: "a slow brown bear"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Put(ctx, "nsB", "k3", facts.BackendDoc{Key: "k3", Content: "unrelated content"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get round-trips; unknown key is ok=false.
	got, ok, err := b.Get(ctx, "nsA", "k1")
	if err != nil || !ok || got.Content != "the quick brown fox" {
		t.Fatalf("Get(nsA,k1) = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := b.Get(ctx, "nsA", "missing"); ok {
		t.Fatalf("Get(missing) should be ok=false")
	}

	// Empty query returns nothing (no terms to match).
	if hits, _ := b.Search(ctx, "nsA", "   ", 4); len(hits) != 0 {
		t.Fatalf("empty query returned %d, want 0", len(hits))
	}
	// "brown" matches both nsA docs; k-truncation to 1 keeps the best-first single.
	both, err := b.Search(ctx, "nsA", "brown", 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("'brown' should match 2 docs, got %d", len(both))
	}
	one, err := b.Search(ctx, "nsA", "brown", 1)
	if err != nil {
		t.Fatalf("Search k=1: %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("k=1 truncation broken: got %d", len(one))
	}
	// A query that matches nothing yields no hits (overlap == 0 path).
	if hits, _ := b.Search(ctx, "nsA", "zzzznotoken", 4); len(hits) != 0 {
		t.Fatalf("no-match query returned %d, want 0", len(hits))
	}
	// nsB is isolated at the raw layer: a nsA term does not reach into nsB.
	if hits, _ := b.Search(ctx, "nsB", "brown", 4); len(hits) != 0 {
		t.Fatalf("cross-namespace leak: nsB matched a nsA term")
	}

	// Scan enumerates one namespace; ExportAll spans all three docs.
	if s, _ := b.Scan(ctx, "nsA"); len(s) != 2 {
		t.Fatalf("Scan(nsA) = %d, want 2", len(s))
	}
	all, err := b.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ExportAll = %d, want 3 (cross-namespace)", len(all))
	}

	// Delete (unknown key ignored) and Namespaces.
	if err := b.Delete(ctx, "nsA", []string{"k1", "nope"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s, _ := b.Scan(ctx, "nsA"); len(s) != 1 {
		t.Fatalf("after Delete Scan(nsA) = %d, want 1", len(s))
	}
	if ns := b.Namespaces(); len(ns) != 2 {
		t.Fatalf("Namespaces = %v, want 2 buckets", ns)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestTokenSet(t *testing.T) {
	// Lowercases, splits on non-alphanumerics, dedupes.
	got := tokenSet("The Quick, brown-FOX the")
	want := map[string]struct{}{"the": {}, "quick": {}, "brown": {}, "fox": {}}
	if len(got) != len(want) {
		t.Fatalf("tokenSet size = %d (%v), want %d", len(got), got, len(want))
	}
	for term := range want {
		if _, ok := got[term]; !ok {
			t.Fatalf("tokenSet missing %q: %v", term, got)
		}
	}
	// Empty / punctuation-only input yields an empty set.
	if s := tokenSet("  ,.!  "); len(s) != 0 {
		t.Fatalf("tokenSet(punct) = %v, want empty", s)
	}
}

func TestNowMinusDaysHelper(t *testing.T) {
	// The suite's date helper: d days back is strictly earlier than now and the
	// gap is ~d days.
	past := nowMinusDays(10)
	if !past.Before(nowMinusDays(1)) {
		t.Fatalf("nowMinusDays(10) should be before nowMinusDays(1)")
	}
	if got := daysDur(2); got.Hours() != 48 {
		t.Fatalf("daysDur(2) = %v, want 48h", got)
	}
}
