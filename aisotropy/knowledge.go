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
	// Content is the document's plain-text body. For binary formats (PDF,
	// HTML) use App.IngestFile, which extracts text via the WithExtractor seam
	// (github.com/ledongthuc/pdf for PDF, golang.org/x/net/html for HTML) and
	// then feeds it through this same pipeline — so this field stays plain text
	// in. A consumer can still pre-extract and set Content themselves; the
	// built-in extraction is additive, not required.
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
		// A consumer's WithVectorBackend (Tier-2 open-core) wins: it is sealed into
		// a tenant-isolating semantic.Store here — the ONE shared build path — so the
		// chat/HTTP RAG surface and the SDK IngestKnowledge surface use the same
		// sealed index (never two divergent stores). The sealed decorator enforces
		// per-tenant isolation by namespace, so physical dedicated-tenant tier
		// routing is not layered over it (the backend owns its persistence); logged
		// when both are configured. Unset => the env/default in-memory/Qdrant store.
		var store semantic.Store
		var err error
		if a.ext.vectorBackend != nil {
			store = semantic.NewVectorBackendStore(a.ext.vectorBackend, emb.Dimension())
			if len(dedicatedTenants) > 0 {
				slog.Warn("semantic tier routing not applied to a WithVectorBackend store (sealed decorator provides per-tenant isolation; the community backend owns its own persistence)", "dedicated", dedicatedTenants)
			}
			slog.Info("custom vector backend installed (WithVectorBackend); sealed into a tenant-isolating semantic store", "dimension", emb.Dimension())
		} else {
			store, err = openSemanticStore(ctx, tierRouter, dedicatedTenants, emb.Dimension())
		}
		// Defensive: never memoize a store when open failed — so App.Close's
		// `if semanticStore != nil` guard is always correct (belt-and-suspenders
		// with openSemanticStore's typed-nil normalization above).
		if err != nil {
			store = nil
		}
		a.semanticStore = store
		a.semanticErr = err
	})
	return a.semanticStore, a.embedder, a.semanticErr
}

// Close releases resources this App lazily memoized outside of Run's own
// lifecycle. There are two such resources, in priority order:
//
//   - a Turn-built runtime (turn.go): if App.Turn was ever called, the App owns
//     a full in-process runtime — the memory tiers, background workers, MCP
//     clients, and tracer. Its teardown (rt.close) stops the workers before
//     closing the stores they write to and shuts tracing, exactly as Run's
//     graceful shutdown does. Because that runtime's semantic store IS the one
//     ensureSemantic memoized on the App, rt.close already closes it — so Close
//     delegates entirely and does NOT also close it a second time.
//   - otherwise, the lazily-built semantic store alone: the SDK-only path
//     (IngestKnowledge/IngestTenantKnowledge/DeleteKnowledge/DeleteTenantKnowledge
//     on an App that called neither Turn nor Run). A Qdrant gRPC connection when
//     QDRANT_HOST is configured; a no-op for the default in-memory store.
//
// Run() manages its own graceful-shutdown lifecycle and tears down its OWN
// (Run-local) runtime before returning, so a Run-served App does not need Close;
// Close matters for embedded consumers that use Turn and/or the SDK ingestion
// methods and should `defer app.Close()` so resources are released when the App
// goes out of scope.
//
// Idempotent (closeOnce: a repeat call returns the same result without closing
// twice) and nil-safe (an App that built neither a runtime nor a semantic store
// returns nil without touching anything). Close must not race a concurrent Turn
// call that is still building the runtime — call it during teardown, after all
// Turn calls have returned.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		if a.runtime != nil {
			// A Turn-built runtime owns closing every store (including the
			// semantic store below), stopping the workers, and shutting tracing.
			// Give it a bounded deadline like Run's graceful shutdown does.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			a.runtime.close(shutdownCtx)
			return
		}
		if a.semanticStore != nil {
			a.closeErr = a.semanticStore.Close()
		}
	})
	return a.closeErr
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
