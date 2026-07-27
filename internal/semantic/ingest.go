// Document ingestion (ADR-0018): chunk -> embed -> upsert as KindDocument
// vectors, tenant/user-scoped and idempotent by construction. This is the
// framework's answer to LangChain document loaders / Mastra semantic recall
// — a caller supplies plain text (parsing PDFs/HTML/etc. is their concern,
// per the ADR's non-goals); this file turns it into retrievable, isolated
// knowledge.

package semantic

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNilStore is returned by Ingest when store is nil: a clear error, never
// a silent no-op (ADR-0018's "Mandatory" clause).
var ErrNilStore = errors.New("semantic: ingest requires a non-nil Store")

// ErrNilEmbedder is returned by Ingest when emb is nil: a clear error, never
// a silent no-op (ADR-0018's "Mandatory" clause).
var ErrNilEmbedder = errors.New("semantic: ingest requires a non-nil Embedder")

// Document is one piece of caller-supplied knowledge to ingest into the
// semantic tier as KindDocument points (ADR-0018). Content is plain text —
// parsing (PDF/HTML extraction, etc.) is the caller's concern, not this
// package's.
type Document struct {
	// ID is the caller-chosen document identifier. Re-ingesting the same ID
	// replaces its previously ingested chunks in place: PointID is derived
	// from (Scope, KindDocument, ID+"#"+chunkIndex), so the same chunk index
	// always maps to the same point id.
	ID string
	// Content is the document's plain-text body, split into chunks by
	// Ingest per ChunkOptions.
	Content string
	// Source is optional provenance (url, filename, section) carried onto
	// every chunk's Point.Citation, for citations at retrieval time. Distinct
	// from ID: ID is the chunk-group key Ingest writes onto Point.Source (so
	// Store.DeleteBySource can remove every chunk of this document as a
	// unit), Source is purely a human-facing display string.
	Source string
}

// DefaultChunkSize / DefaultChunkOverlap are ChunkOptions' fallbacks (in
// runes) when a caller leaves Size/Overlap unset — see DefaultChunkOptions.
const (
	DefaultChunkSize    = 512
	DefaultChunkOverlap = 64
)

// ChunkOptions tunes how a Document's Content is split before embedding.
// Size and Overlap are measured in runes, not bytes, so multi-byte UTF-8
// text is never split mid-rune.
type ChunkOptions struct {
	// Size is the target chunk length in runes. <=0 falls back to
	// DefaultChunkSize.
	Size int
	// Overlap is how many trailing runes of one chunk are repeated at the
	// start of the next chunk, so content spanning a chunk boundary still
	// appears whole in at least one chunk. <0 or >=Size falls back to 0 (no
	// overlap), which guarantees chunking always makes forward progress.
	Overlap int
}

// DefaultChunkOptions returns the ADR-0018 default chunk tuning (512 runes,
// 64 overlap).
func DefaultChunkOptions() ChunkOptions {
	return ChunkOptions{Size: DefaultChunkSize, Overlap: DefaultChunkOverlap}
}

// normalize fills in Size's default and clamps Overlap so chunkRunes always
// advances (a caller-supplied Overlap >= Size would otherwise loop forever).
func (o ChunkOptions) normalize() ChunkOptions {
	if o.Size <= 0 {
		o.Size = DefaultChunkSize
	}
	if o.Overlap < 0 || o.Overlap >= o.Size {
		o.Overlap = 0
	}
	return o
}

// chunkRunes splits content into rune-safe chunks of opts.Size runes,
// repeating opts.Overlap trailing runes at the start of each following
// chunk. A document no longer than opts.Size is returned as a single chunk
// (its content unmodified). Empty content yields no chunks.
func chunkRunes(content string, opts ChunkOptions) []string {
	opts = opts.normalize()
	runes := []rune(content)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= opts.Size {
		return []string{content}
	}
	step := opts.Size - opts.Overlap
	chunks := make([]string, 0, len(runes)/step+1)
	for start := 0; start < len(runes); start += step {
		end := start + opts.Size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

// Ingest chunks each Document's Content (per opts), embeds the chunks via
// emb, and upserts one KindDocument Point per chunk into store, scoped to
// sc (ADR-0018). Idempotent, and idempotent even across a SHRINKING
// document: before writing a document's fresh chunks, Ingest first calls
// store.DeleteBySource(sc, KindDocument, doc.ID) to remove every
// previously-ingested chunk of that document id, then upserts the current
// chunk set. Re-ingesting the same content therefore always leaves exactly
// today's chunk set behind — never orphaned chunks from a longer prior
// version (the gap ADR-0018 flagged; closed here rather than relying on
// PointID collisions, which only cover a same-or-larger chunk count). A nil
// store or nil embedder is a clear error, never a silent no-op. Returns the
// total number of chunks written across all docs.
func Ingest(ctx context.Context, store Store, emb Embedder, sc Scope, opts ChunkOptions, docs []Document) (int, error) {
	if store == nil {
		return 0, ErrNilStore
	}
	if emb == nil {
		return 0, ErrNilEmbedder
	}

	now := time.Now().UTC()
	var points []Point
	for _, doc := range docs {
		if _, err := store.DeleteBySource(ctx, sc, KindDocument, doc.ID); err != nil {
			return 0, fmt.Errorf("semantic: delete stale chunks for document %q: %w", doc.ID, err)
		}

		chunks := chunkRunes(doc.Content, opts)
		if len(chunks) == 0 {
			continue
		}
		// Passage role: stored KindDocument chunks carry the e5 "passage: "
		// prefix on an e5 embedder (ADR-0054), matching the "query: "-prefixed
		// search side. A non-e5/external embedder is unaffected (plain Embed).
		vecs, err := EmbedPassage(ctx, emb, chunks)
		if err != nil {
			return 0, fmt.Errorf("semantic: embed document %q: %w", doc.ID, err)
		}
		if len(vecs) != len(chunks) {
			return 0, fmt.Errorf("semantic: embedder returned %d vectors for %d chunks of document %q", len(vecs), len(chunks), doc.ID)
		}
		for i, chunk := range chunks {
			points = append(points, Point{
				ID:         PointID(sc, KindDocument, fmt.Sprintf("%s#%d", doc.ID, i)),
				TenantID:   sc.Tenant(),
				UserID:     sc.User(),
				Kind:       KindDocument,
				Content:    chunk,
				ChunkIndex: i,
				Source:     doc.ID,
				Citation:   doc.Source,
				Vector:     vecs[i],
				CreatedAt:  now,
			})
		}
	}
	if len(points) == 0 {
		return 0, nil
	}
	if err := store.Upsert(ctx, sc, points); err != nil {
		return 0, fmt.Errorf("semantic: upsert ingested chunks: %w", err)
	}
	return len(points), nil
}
