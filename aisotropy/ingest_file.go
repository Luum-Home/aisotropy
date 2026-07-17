package aisotropy

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/ingest"
)

// Extractor turns raw document bytes (a PDF, an HTML page, a plain-text file)
// into the plain text IngestFile then feeds through the normal
// chunk->embed->store knowledge pipeline. It is the façade's re-export of the
// real internal/ingest.Extractor, so a consumer's implementation is the exact
// contract the runtime consumes — the same aliasing discipline as Embedder.
//
// Supply your own via WithExtractor to handle formats the built-in extractor
// does not (scanned/image PDFs via OCR, DOCX, a remote extraction API); leave
// it unset to get ingest.DefaultExtractor() (PDF via github.com/ledongthuc/pdf,
// HTML via golang.org/x/net/html, plain text passthrough).
type Extractor = ingest.Extractor

// ErrEmptyContent is returned (wrapped) when a file parses but carries no
// extractable text — most commonly a scanned/image-only PDF, which needs OCR
// (out of scope for the built-in extractor). Re-exported so consumers can
// errors.Is against it to route such files to their own OCR Extractor.
var ErrEmptyContent = ingest.ErrEmptyContent

// FileDocument is one binary document to ingest via IngestFile: its raw bytes
// plus an optional MIME ContentType hint. It is the file-oriented sibling of
// Document (which is plain text in). Extraction turns it into a Document —
// {ID, Content: <extracted text>, Source} — which then flows through the exact
// same scoped IngestKnowledge pipeline, so there is no separate, unscoped
// ingestion path.
type FileDocument struct {
	// ID is the caller-chosen document identifier; re-ingesting the same ID
	// idempotently replaces its previously ingested chunks (same semantics as
	// Document.ID).
	ID string
	// Data is the raw document bytes (PDF, HTML, or plain text).
	Data []byte
	// ContentType is an optional MIME hint (e.g. "application/pdf",
	// "text/html"). Empty means "sniff it from the bytes" — a %PDF- magic
	// prefix or an HTML doctype/<html> lead is detected automatically, with
	// plain text as the fallback.
	ContentType string
	// Source is optional provenance (filename, url, section) surfaced on
	// retrieved chunks for citations (same as Document.Source).
	Source string
}

// WithExtractor overrides the byte->text extractor IngestFile uses (default:
// ingest.DefaultExtractor(), covering PDF + HTML + plain text). This is the
// swappable-parser seam, consistent with WithEmbedder / WithVectorBackend: a
// consumer can plug an OCR pipeline, a DOCX parser, or a remote extraction
// service without forking. The extractor is tenant-agnostic (bytes in, text
// out) — it never sees a scope, so overriding it cannot affect tenant
// isolation.
func WithExtractor(e Extractor) Option {
	return func(a *App) { a.extractor = e }
}

// extractorOrDefault resolves the configured extractor, falling back to the
// built-in DefaultExtractor when WithExtractor was never called.
func (a *App) extractorOrDefault() Extractor {
	if a.extractor != nil {
		return a.extractor
	}
	return ingest.DefaultExtractor()
}

// IngestFile extracts plain text from each binary document (detecting/parsing
// PDF, HTML, or plain text) and ingests it through the SAME scoped
// chunk->embed->store pipeline as IngestKnowledge, scoped to (tenant, user).
// It is purely additive: IngestKnowledge (plain text in) is unchanged, and
// IngestFile is a thin extract-then-delegate wrapper over it, so file ingestion
// inherits IngestKnowledge's scope validation (ADR-0011), idempotent
// re-ingest-by-ID, chunk tuning, and BYO-embedder behavior with no new
// unscoped write path.
//
// Extraction runs through the WithExtractor seam (default: PDF + HTML + plain
// text). A document that parses but has no text — typically a scanned/image
// PDF, which needs OCR (out of scope for the default extractor) — fails with an
// error wrapping ErrEmptyContent; nothing is ingested for it. Returns the total
// number of chunks written across all docs.
func (a *App) IngestFile(ctx context.Context, tenant, user string, docs ...FileDocument) (int, error) {
	textDocs, err := a.extractFileDocs(ctx, "ingest file", docs)
	if err != nil {
		return 0, err
	}
	return a.IngestKnowledge(ctx, tenant, user, textDocs...)
}

// IngestTenantFile is the tenant-shared-bucket counterpart of IngestFile
// (ADR-0019): it extracts text from each binary document and ingests it via
// IngestTenantKnowledge, retrievable by every user of the tenant. Same
// extraction, same additive contract as IngestFile.
func (a *App) IngestTenantFile(ctx context.Context, tenant string, docs ...FileDocument) (int, error) {
	textDocs, err := a.extractFileDocs(ctx, "ingest tenant file", docs)
	if err != nil {
		return 0, err
	}
	return a.IngestTenantKnowledge(ctx, tenant, textDocs...)
}

// extractFileDocs runs each FileDocument's bytes through the configured
// extractor and maps the result onto plain-text Documents, preserving ID and
// Source. op names the calling method for error context.
func (a *App) extractFileDocs(ctx context.Context, op string, docs []FileDocument) ([]Document, error) {
	ex := a.extractorOrDefault()
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		text, err := ex.Extract(ctx, d.Data, d.ContentType)
		if err != nil {
			return nil, fmt.Errorf("aisotropy: %s: extract %q: %w", op, d.ID, err)
		}
		out = append(out, Document{ID: d.ID, Content: text, Source: d.Source})
	}
	return out, nil
}
