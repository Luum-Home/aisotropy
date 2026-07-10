// Package aisotropy: the SDK arm of the knowledge-ingestion surface
// (ADR-0018) — BYO embedder, chunk tuning, and App.IngestKnowledge. The HTTP
// arm (internal/api.RegisterKnowledge) is wired into Run in app.go.
package aisotropy

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// Embedder maps text to vectors for the semantic tier. Supply a real model
// via WithEmbedder, or set AISOTROPY_EMBED_URL to point semantic.EmbedderFromEnv
// at an OpenAI-compatible /v1/embeddings endpoint — OpenAI, OpenRouter, or a
// fully local server (Ollama, text-embeddings-inference, llama.cpp's server)
// — via semantic.APIEmbedder. With neither set, the default is a
// deterministic HashEmbedder placeholder (semantic.NewHashEmbedder(0)) that
// wires and tests the ingestion/RAG path but does not rank semantically —
// see ADR-0018's "BYO embedder" decision. It is an alias for the real
// internal/semantic type, so a consumer's Embedder is the exact contract the
// runtime's semantic store and hydrateSystemPrompt RAG hydration use.
type Embedder = semantic.Embedder

// Document is one piece of knowledge to ingest via IngestKnowledge or
// POST .../knowledge. It is the façade's own type (not internal/semantic's)
// so the adoption surface never leaks an internal/ type across the boundary
// (ADR-0016's "sealed runtime" invariant); IngestKnowledge maps it 1:1 onto
// semantic.Document.
type Document struct {
	// ID is the caller-chosen document identifier; re-ingesting the same ID
	// idempotently replaces its previously ingested chunks.
	ID string
	// Content is the document's plain-text body. Parsing (PDF/HTML/etc.
	// extraction) is the caller's concern — this is plain text in.
	Content string
	// Source is optional provenance (url, filename, section) surfaced on
	// retrieved chunks for citations.
	Source string
}

// WithEmbedder overrides the semantic-tier embedder (default:
// semantic.NewHashEmbedder(0), a deterministic dev/test placeholder). Also
// used by IngestKnowledge and the chat/chat-stream routes' semantic RAG
// hydration, so retrieval quality only becomes meaningful once a real model
// is plugged in behind this seam (ADR-0018).
func WithEmbedder(e Embedder) Option {
	return func(a *App) { a.embedder = e }
}

// WithChunkSize tunes IngestKnowledge/POST .../knowledge's chunking, in
// runes (ADR-0018). Default when unset: 512/64
// (semantic.DefaultChunkOptions()).
func WithChunkSize(size, overlap int) Option {
	return func(a *App) {
		a.chunkSize = size
		a.chunkOverlap = overlap
	}
}

// chunkOptions resolves the App's configured chunk tuning, falling back to
// semantic.DefaultChunkOptions() when WithChunkSize was never called (a.
// chunkSize left at its zero value).
func (a *App) chunkOptions() semantic.ChunkOptions {
	if a.chunkSize <= 0 {
		return semantic.DefaultChunkOptions()
	}
	return semantic.ChunkOptions{Size: a.chunkSize, Overlap: a.chunkOverlap}
}

// ensureSemantic lazily builds, exactly once, the embedder + semantic store
// this App uses, and memoizes them on the App so IngestKnowledge works
// whether it is called before Run boots the HTTP runtime (pure
// composition-SDK usage) or afterward, alongside a running server: Run
// itself calls ensureSemantic instead of building its own store, so both
// surfaces always share one semantic index per App, never two independently
// evolving ones.
//
// Embedder selection precedence (ADR-0018, extended for the BYO API
// embedder): an explicit WithEmbedder always wins; otherwise
// semantic.EmbedderFromEnv() resolves a real APIEmbedder when
// AISOTROPY_EMBED_URL is configured, or falls back to the deterministic
// HashEmbedder placeholder when it is not. So: WithEmbedder > env-configured
// APIEmbedder > HashEmbedder.
func (a *App) ensureSemantic(ctx context.Context) (semantic.Store, semantic.Embedder, error) {
	a.semanticOnce.Do(func() {
		emb := a.embedder
		if emb == nil {
			emb = semantic.EmbedderFromEnv()
		}
		a.embedder = emb
		// Never log the API key (AISOTROPY_EMBED_API_KEY) — only the
		// non-secret shape of the active embedder.
		slog.Info("semantic embedder selected", "type", fmt.Sprintf("%T", emb), "dimension", emb.Dimension())

		// Physical multi-tenancy (ADR-0007), the same routing Run applies to
		// the facts/semantic stores: tenants in AISOTROPY_DEDICATED_TENANTS
		// get their own physical semantic resource.
		dedicatedTenants := splitCSV(os.Getenv("AISOTROPY_DEDICATED_TENANTS"))
		tierRouter := routing.NewRouter(routing.NewStaticPolicy(dedicatedTenants...))
		store, err := openSemanticStore(ctx, tierRouter, dedicatedTenants, emb.Dimension())
		a.semanticStore = store
		a.semanticErr = err
	})
	return a.semanticStore, a.embedder, a.semanticErr
}

// IngestKnowledge chunks, embeds, and stores docs as KindDocument vectors
// scoped to (tenant, user) — the SDK arm of the dual-surface ingestion
// capability (ADR-0018; the HTTP arm is POST .../knowledge). tenant/user are
// validated through semantic.NewScope (ADR-0011): a bad scope is returned as
// an error, never silently accepted. A nil store/embedder (only reachable if
// ensureSemantic itself failed, e.g. a misconfigured QDRANT_HOST) is
// likewise a returned error, never a silent no-op. Returns the number of
// chunks written across all docs.
func (a *App) IngestKnowledge(ctx context.Context, tenant, user string, docs ...Document) (int, error) {
	sc, err := semantic.NewScope(tenant, user) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest knowledge: %w", err)
	}
	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest knowledge: build semantic store: %w", err)
	}

	internalDocs := make([]semantic.Document, 0, len(docs))
	for _, d := range docs {
		internalDocs = append(internalDocs, semantic.Document{ID: d.ID, Content: d.Content, Source: d.Source})
	}

	n, err := semantic.Ingest(ctx, store, emb, sc, a.chunkOptions(), internalDocs)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest knowledge: %w", err)
	}
	return n, nil
}

// DeleteKnowledge removes every previously ingested chunk of docID within
// (tenant, user) — the SDK arm of ADR-0018's open question #3, the HTTP arm
// being DELETE .../user/{user_id}/knowledge/{doc_id}. Idempotent: deleting
// an unknown or already-deleted docID returns (0, nil), never an error.
// Returns the number of chunks removed.
func (a *App) DeleteKnowledge(ctx context.Context, tenant, user, docID string) (int, error) {
	sc, err := semantic.NewScope(tenant, user) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete knowledge: %w", err)
	}
	store, _, err := a.ensureSemantic(ctx)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete knowledge: build semantic store: %w", err)
	}
	n, err := store.DeleteBySource(ctx, sc, semantic.KindDocument, docID)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete knowledge: %w", err)
	}
	return n, nil
}

// IngestTenantKnowledge chunks, embeds, and stores docs as KindDocument
// vectors in the tenant-shared bucket (ADR-0019; semantic.NewTenantScope, an
// empty-user Scope) — the SDK arm of the dual-surface tenant-wide sharing
// capability, the HTTP arm being POST /v1/tenant/{tenant_id}/knowledge. A
// document ingested here is retrievable by every user of tenant via
// hydrateSystemPrompt's merged search, with no per-user re-ingestion.
// tenant is validated through semantic.NewTenantScope: a bad tenant is
// returned as an error, never silently accepted. Returns the number of
// chunks written across all docs.
func (a *App) IngestTenantKnowledge(ctx context.Context, tenant string, docs ...Document) (int, error) {
	sc, err := semantic.NewTenantScope(tenant) //nolint:contextcheck // semantic.NewTenantScope has no context parameter (internal/semantic, out of scope for this cleanup)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest tenant knowledge: %w", err)
	}
	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest tenant knowledge: build semantic store: %w", err)
	}

	internalDocs := make([]semantic.Document, 0, len(docs))
	for _, d := range docs {
		internalDocs = append(internalDocs, semantic.Document{ID: d.ID, Content: d.Content, Source: d.Source})
	}

	n, err := semantic.Ingest(ctx, store, emb, sc, a.chunkOptions(), internalDocs)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest tenant knowledge: %w", err)
	}
	return n, nil
}

// DeleteTenantKnowledge removes every previously ingested chunk of docID
// from the tenant-shared bucket (ADR-0019 open question #1) — the SDK arm,
// the HTTP arm being DELETE /v1/tenant/{tenant_id}/knowledge/{doc_id}.
// Idempotent: deleting an unknown or already-deleted docID returns (0, nil),
// never an error. Returns the number of chunks removed.
func (a *App) DeleteTenantKnowledge(ctx context.Context, tenant, docID string) (int, error) {
	sc, err := semantic.NewTenantScope(tenant) //nolint:contextcheck // semantic.NewTenantScope has no context parameter (internal/semantic, out of scope for this cleanup)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete tenant knowledge: %w", err)
	}
	store, _, err := a.ensureSemantic(ctx)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete tenant knowledge: build semantic store: %w", err)
	}
	n, err := store.DeleteBySource(ctx, sc, semantic.KindDocument, docID)
	if err != nil {
		return 0, fmt.Errorf("aisotropy: delete tenant knowledge: %w", err)
	}
	return n, nil
}
