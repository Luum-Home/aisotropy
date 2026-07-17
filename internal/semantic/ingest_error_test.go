package semantic

import (
	"context"
	"errors"
	"testing"
)

// errStore wraps InMemoryStore to inject a failure on a chosen method, so
// Ingest's error branches (DeleteBySource before re-upsert, and the final
// Upsert) run with the stdlib only — no real vector engine.
type errStore struct {
	*InMemoryStore
	deleteBySourceErr error
	upsertErr         error
}

func (s errStore) DeleteBySource(ctx context.Context, sc Scope, kind Kind, source string) (int, error) {
	if s.deleteBySourceErr != nil {
		return 0, s.deleteBySourceErr
	}
	return s.InMemoryStore.DeleteBySource(ctx, sc, kind, source)
}

func (s errStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return s.InMemoryStore.Upsert(ctx, sc, points)
}

// countMismatchEmbedder returns fewer vectors than requested (or a hard error),
// to exercise Ingest's embed-error and vector-count-mismatch guards.
type countMismatchEmbedder struct {
	dim     int
	err     error
	shortBy int // omit this many trailing vectors from the result
}

func (e countMismatchEmbedder) Dimension() int { return e.dim }

func (e countMismatchEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	n := len(texts) - e.shortBy
	if n < 0 {
		n = 0
	}
	out := make([][]float32, n)
	for i := range out {
		out[i] = make([]float32, e.dim)
	}
	return out, nil
}

// TestIngestDeleteBySourceErrorPropagates verifies a failure clearing a
// document's stale chunks aborts the ingest with a wrapped error, never a
// silent partial write.
func TestIngestDeleteBySourceErrorPropagates(t *testing.T) {
	sentinel := errors.New("delete boom")
	emb := NewHashEmbedder(0)
	st := errStore{InMemoryStore: NewInMemoryStore(emb.Dimension()), deleteBySourceErr: sentinel}
	sc := Scope{tenant: "acme", user: "alice"}

	_, err := Ingest(context.Background(), st, emb, sc, DefaultChunkOptions(), []Document{{ID: "d", Content: "text"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Ingest err = %v, want a wrap of the DeleteBySource error", err)
	}
}

// TestIngestEmbedErrorPropagates verifies an embedder failure aborts the ingest
// with a wrapped error.
func TestIngestEmbedErrorPropagates(t *testing.T) {
	sentinel := errors.New("embed boom")
	st := NewInMemoryStore(4)
	emb := countMismatchEmbedder{dim: 4, err: sentinel}
	sc := Scope{tenant: "acme", user: "alice"}

	_, err := Ingest(context.Background(), st, emb, sc, DefaultChunkOptions(), []Document{{ID: "d", Content: "text"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Ingest err = %v, want a wrap of the Embed error", err)
	}
}

// TestIngestVectorCountMismatchIsError verifies an embedder that returns a
// different number of vectors than chunks is a hard error (the misalignment
// guard), never a truncated/misaligned upsert.
func TestIngestVectorCountMismatchIsError(t *testing.T) {
	st := NewInMemoryStore(4)
	emb := countMismatchEmbedder{dim: 4, shortBy: 1} // one fewer vector than chunks
	sc := Scope{tenant: "acme", user: "alice"}

	// A short doc chunks to exactly 1 chunk; shortBy:1 -> 0 vectors for 1 chunk.
	_, err := Ingest(context.Background(), st, emb, sc, DefaultChunkOptions(), []Document{{ID: "d", Content: "short doc"}})
	if err == nil {
		t.Fatal("Ingest: expected a vector-count mismatch error, got nil")
	}
}

// TestIngestUpsertErrorPropagates verifies a failure writing the freshly
// embedded chunks aborts the ingest with a wrapped error.
func TestIngestUpsertErrorPropagates(t *testing.T) {
	sentinel := errors.New("upsert boom")
	emb := NewHashEmbedder(0)
	st := errStore{InMemoryStore: NewInMemoryStore(emb.Dimension()), upsertErr: sentinel}
	sc := Scope{tenant: "acme", user: "alice"}

	_, err := Ingest(context.Background(), st, emb, sc, DefaultChunkOptions(), []Document{{ID: "d", Content: "text to embed"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Ingest err = %v, want a wrap of the Upsert error", err)
	}
}

// TestChunkRunesDefaultsSizeWhenNonPositive verifies normalize's Size<=0
// fallback: a ChunkOptions with an unset Size chunks using DefaultChunkSize
// rather than looping or producing zero-length chunks.
func TestChunkRunesDefaultsSizeWhenNonPositive(t *testing.T) {
	// Size 0 -> DefaultChunkSize (512). A sub-512-rune doc is a single chunk.
	got := chunkRunes("a modest document body", ChunkOptions{Size: 0, Overlap: 0})
	if len(got) != 1 {
		t.Fatalf("chunkRunes(Size=0) produced %d chunks, want 1 (defaulted to %d)", len(got), DefaultChunkSize)
	}
}
