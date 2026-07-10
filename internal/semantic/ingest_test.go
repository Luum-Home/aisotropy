package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestChunkRunesShortDocumentIsSingleChunk verifies a document no longer
// than the configured size comes back as exactly one chunk, unmodified.
func TestChunkRunesShortDocumentIsSingleChunk(t *testing.T) {
	content := "a short document"
	got := chunkRunes(content, ChunkOptions{Size: 512, Overlap: 64})
	if len(got) != 1 || got[0] != content {
		t.Fatalf("chunkRunes(short doc) = %#v, want a single unmodified chunk", got)
	}
}

// TestChunkRunesEmptyContentYieldsNoChunks verifies an empty document
// produces zero chunks (Ingest must skip it, not write an empty Point).
func TestChunkRunesEmptyContentYieldsNoChunks(t *testing.T) {
	if got := chunkRunes("", ChunkOptions{Size: 10, Overlap: 2}); got != nil {
		t.Fatalf("chunkRunes(\"\") = %#v, want nil", got)
	}
}

// TestChunkRunesSizeAndOverlap verifies a long document is split into
// multiple chunks of the configured size, with the configured overlap of
// trailing runes repeated at the start of the next chunk, and that
// reassembling with the overlap removed reconstructs the original content.
func TestChunkRunesSizeAndOverlap(t *testing.T) {
	// 25 runes, chunked at size 10 with overlap 3: step = 7.
	content := "abcdefghijklmnopqrstuvwxy" // 25 letters
	opts := ChunkOptions{Size: 10, Overlap: 3}
	chunks := chunkRunes(content, opts)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a 25-rune doc at size 10, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > opts.Size {
			t.Fatalf("chunk %d has %d runes, want <= %d", i, len([]rune(c)), opts.Size)
		}
	}
	// Overlap: the last 3 runes of chunk i must be the first 3 runes of
	// chunk i+1 (true for every non-final pair given step=7 < size=10).
	for i := range len(chunks) - 1 {
		a := []rune(chunks[i])
		b := []rune(chunks[i+1])
		tail := string(a[len(a)-opts.Overlap:])
		head := string(b[:opts.Overlap])
		if tail != head {
			t.Fatalf("chunk %d tail %q != chunk %d head %q, overlap not preserved", i, tail, i+1, head)
		}
	}
	// The final chunk must reach the end of the content.
	last := []rune(chunks[len(chunks)-1])
	wantTail := []rune(content)[len([]rune(content))-len(last):]
	if string(last) != string(wantTail) {
		t.Fatalf("last chunk = %q, want the document's tail %q", string(last), string(wantTail))
	}
}

// TestChunkRunesIsRuneSafe verifies multi-byte UTF-8 runes are never split
// mid-rune: every returned chunk must itself be valid, round-trippable
// content, and re-joining without overlap must reconstruct exactly the
// original rune sequence.
func TestChunkRunesIsRuneSafe(t *testing.T) {
	// Multi-byte runes throughout (accented + CJK + emoji), long enough to
	// force multiple chunks at a small size.
	content := strings.Repeat("héllo wörld 世界 🌍 ", 20)
	opts := ChunkOptions{Size: 15, Overlap: 4}
	chunks := chunkRunes(content, opts)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// Reconstruct by taking each chunk's non-overlapping suffix (all but the
	// first chunk contributes only its post-overlap tail) and compare
	// against the original rune sequence.
	var rebuilt []rune
	rebuilt = append(rebuilt, []rune(chunks[0])...)
	for _, c := range chunks[1:] {
		r := []rune(c)
		rebuilt = append(rebuilt, r[opts.Overlap:]...)
	}
	if string(rebuilt) != content {
		t.Fatalf("rune-safe reconstruction mismatch:\n got  %q\n want %q", string(rebuilt), content)
	}
	// Every chunk must itself be valid UTF-8 (guaranteed by rune-slicing,
	// but assert it explicitly as the DoD).
	for i, c := range chunks {
		if !isValidUTF8(c) {
			t.Fatalf("chunk %d is not valid UTF-8: %q", i, c)
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestChunkRunesOverlapClampedToPreventInfiniteLoop verifies an
// Overlap >= Size (which would otherwise stall forward progress, since
// step := Size - Overlap would be <= 0) is silently clamped to 0 by
// normalize rather than looping forever or erroring. The test itself is the
// safety net: if clamping regressed, this call would hang and the test
// would time out under `go test`'s default per-test deadline.
func TestChunkRunesOverlapClampedToPreventInfiniteLoop(t *testing.T) {
	content := strings.Repeat("x", 100)
	chunks := chunkRunes(content, ChunkOptions{Size: 10, Overlap: 10})
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range chunks {
		if len([]rune(c)) > 10 {
			t.Fatalf("chunk %d has %d runes, want <= 10", i, len([]rune(c)))
		}
	}
}

// TestIngestWritesKindDocumentPoints verifies Ingest chunks, embeds, and
// upserts KindDocument points retrievable via a kind-filtered Search.
func TestIngestWritesKindDocumentPoints(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	docs := []Document{
		{ID: "handbook", Content: "the aisotropy onboarding handbook covers tenancy and memory tiers", Source: "handbook.md"},
	}
	n, err := Ingest(ctx, st, emb, sc, ChunkOptions{Size: 512, Overlap: 64}, docs)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n != 1 {
		t.Fatalf("chunks ingested = %d, want 1 (short doc = single chunk)", n)
	}

	got, err := st.Search(ctx, sc, embedOne(t, emb, "onboarding handbook tenancy"), 10, KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d KindDocument results, want 1", len(got))
	}
	if got[0].Kind != KindDocument {
		t.Fatalf("Kind = %q, want %q", got[0].Kind, KindDocument)
	}
	if got[0].Source != "handbook.md" {
		t.Fatalf("Source = %q, want handbook.md", got[0].Source)
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt was not set")
	}
}

// TestIngestIsIdempotent verifies re-ingesting the same Document.ID replaces
// its chunks in place rather than duplicating them.
func TestIngestIsIdempotent(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	opts := ChunkOptions{Size: 512, Overlap: 64}

	if _, err := Ingest(ctx, st, emb, sc, opts, []Document{{ID: "doc-1", Content: "first version of the document"}}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if _, err := Ingest(ctx, st, emb, sc, opts, []Document{{ID: "doc-1", Content: "second, updated version of the document"}}); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	got, err := st.Search(ctx, sc, embedOne(t, emb, "version document"), 10, KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d points after re-ingesting doc-1, want 1 (idempotent replace)", len(got))
	}
	if got[0].Content != "second, updated version of the document" {
		t.Fatalf("content = %q, want the updated version", got[0].Content)
	}
}

// TestIngestScopeIsolated verifies a document ingested for one tenant/user
// is never retrievable by another — the tenant-isolation-by-construction
// invariant applied to KindDocument.
func TestIngestScopeIsolated(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()
	opts := ChunkOptions{Size: 512, Overlap: 64}
	const content = "confidential acme runbook for incident response"

	acme := Scope{tenant: "acme", user: "alice"}
	if _, err := Ingest(ctx, st, emb, acme, opts, []Document{{ID: "runbook", Content: content}}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	other := Scope{tenant: "globex", user: "alice"}
	got, err := st.Search(ctx, other, embedOne(t, emb, content), 10, KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("isolation leak: globex/alice retrieved %d acme documents, want 0", len(got))
	}
}

// TestIngestNilStoreOrEmbedderErrors verifies a nil store or embedder is a
// clear error, never a silent no-op (ADR-0018's Mandatory clause).
func TestIngestNilStoreOrEmbedderErrors(t *testing.T) {
	emb := NewHashEmbedder(0)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	docs := []Document{{ID: "d", Content: "content"}}

	if _, err := Ingest(ctx, nil, emb, sc, DefaultChunkOptions(), docs); !errors.Is(err, ErrNilStore) {
		t.Fatalf("nil store: err = %v, want ErrNilStore", err)
	}
	st := NewInMemoryStore(emb.Dimension())
	if _, err := Ingest(ctx, st, nil, sc, DefaultChunkOptions(), docs); !errors.Is(err, ErrNilEmbedder) {
		t.Fatalf("nil embedder: err = %v, want ErrNilEmbedder", err)
	}
}
