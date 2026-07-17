---
type: Tutorial
title: "05 — Knowledge & RAG"
description: Fill the semantic tier with IngestKnowledge and let turns retrieve it — per-user and tenant-wide, with a real embedder for meaningful ranking.
tags: [tutorial, knowledge, rag, embedder, semantic]
timestamp: 2026-07-16T00:00:00Z
---

# 05 — Knowledge & RAG

**Goal:** give the agent a corpus to draw on. Ingest documents into the semantic tier; turns retrieve
the relevant chunks automatically (retrieval-augmented generation).

`App.IngestKnowledge` chunks, embeds, and stores documents scoped to `(tenant, user)`. Later turns
hydrate the system prompt with the closest chunks by similarity — no extra call on the read side.

```go
app := aisotropy.New(
	// A real embedder makes ranking meaningful — see the honesty note.
	aisotropy.WithEmbedder(myEmbedder),
)
defer func() { _ = app.Close() }()

n, err := app.IngestKnowledge(ctx, "acme", "alice",
	aisotropy.Document{ID: "handbook-1", Content: "Refunds are processed within 5 business days.", Source: "handbook.md"},
)
if err != nil {
	log.Fatal(err)
}
fmt.Printf("ingested %d chunks\n", n)

// A later turn for the same (tenant, user) retrieves the ingested chunk during hydration.
out, _ := app.Turn(ctx, aisotropy.ChatInput{
	TenantID: "acme", UserID: "alice", SessionID: "s1",
	Prompt: "how long do refunds take?",
})
fmt.Println(out.Reply)
```

Scope is not optional here either: `tenant` and `user` are validated when the scope is built, and the
chunks are stored under it — so retrieval only ever returns *this* user's documents. Re-ingesting the
same `ID` idempotently replaces its previous chunks.

For a corpus every user of a tenant should see (a product manual, shared policies), ingest it once at
**tenant scope** — no per-user re-ingestion:

```go
_, _ = app.IngestTenantKnowledge(ctx, "acme",
	aisotropy.Document{ID: "policy-1", Content: "All plans include 24/7 support.", Source: "policies.md"},
)
```

## Ingesting PDFs and HTML (`IngestFile`)

`IngestKnowledge` takes plain text. To ingest a **binary document** — a PDF or an HTML page — use
`App.IngestFile`: it extracts the text and feeds it through the *exact same* scoped
chunk → embed → store pipeline. Nothing else changes: same `(tenant, user)` isolation, same
idempotent re-ingest-by-`ID`, same embedder.

```go
pdf, _ := os.ReadFile("handbook.pdf")

n, err := app.IngestFile(ctx, "acme", "alice", aisotropy.FileDocument{
	ID:          "handbook",
	Data:        pdf,
	ContentType: "application/pdf", // optional — omit to sniff from the bytes
	Source:      "handbook.pdf",
})
```

`ContentType` is a hint; leave it empty and the bytes are sniffed (a `%PDF-` magic prefix, an HTML
doctype). PDF extraction uses [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf)
(BSD-3-Clause) and HTML uses `golang.org/x/net/html` (BSD-3-Clause) — both permissive, in line with the
repo's MIT/Apache/BSD-only policy. There is also `IngestTenantFile` for the tenant-shared bucket.

**Bring your own parser.** Extraction is a swappable seam, just like the embedder. Supply
`aisotropy.WithExtractor(myExtractor)` to override the default — for example an OCR pipeline, a DOCX
parser, or a remote extraction API. Your `Extractor` sees opaque bytes and a MIME hint, never a scope,
so overriding it cannot affect tenant isolation.

**Scanned/image PDFs need OCR.** The built-in extractor reads a PDF's *embedded text layer* only. A
scanned or image-only PDF has none, so `IngestFile` returns an error wrapping
`aisotropy.ErrEmptyContent` rather than silently ingesting nothing — plug an OCR-backed `Extractor` via
`WithExtractor` to handle those.

**Honesty note.** With no `WithEmbedder` and no `AISOTROPY_EMBED_URL`, the default is a deterministic
**HashEmbedder placeholder**: it wires and tests the ingest → retrieve path end-to-end, but it does
**not rank semantically**. Retrieval quality only becomes meaningful once you plug in a real model —
`WithEmbedder(...)`, or set `AISOTROPY_EMBED_URL` to any OpenAI-compatible `/v1/embeddings` endpoint
(OpenAI, OpenRouter, or a fully local Ollama / text-embeddings-inference server).

**Runnable example:** [`knowledge-rag`](../../examples/knowledge-rag) — ingest a small corpus, then ask
a question it answers.

**Depth:** [ADR-0018 — knowledge-ingestion surface](../decisions/0018-knowledge-ingestion-surface.md)
and [ADR-0019 — tenant-wide document sharing](../decisions/0019-tenant-wide-document-sharing.md).

Next: [06 — Sandboxed tasks](06-sandboxed-task.md).
