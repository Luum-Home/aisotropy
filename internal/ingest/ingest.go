// Package ingest is the document text-extraction seam that sits IN FRONT of the
// semantic tier's chunk->embed->store pipeline (internal/semantic.Ingest,
// ADR-0018). The knowledge surface historically accepted plain text only —
// "Parsing (PDF/HTML/etc.) is the caller's responsibility" — so a consumer who
// wanted to ingest a PDF had to run their own extractor first. This package
// closes that gap with a first-class, tenant-agnostic byte->text step.
//
// It deliberately owns NO tenant/user/scope concept: an Extractor turns opaque
// bytes into plain text and nothing more. Scoping, chunking, embedding, and
// storage stay entirely with the caller (aisotropy.App.IngestFile feeds the
// extracted text straight into the SAME scoped IngestKnowledge pipeline), so
// adding extraction introduces no new unscoped write path and cannot weaken the
// "every scope is built by the runtime from ctx" isolation invariant (ADR-0011).
//
// Extractor is a swappable interface, mirroring the BYO-embedder /
// WithVectorBackend pattern: DefaultExtractor() handles PDF, HTML, and plain
// text out of the box, but a consumer can supply their own (an OCR pipeline for
// scanned PDFs, a DOCX parser, a cloud extraction API) via
// aisotropy.WithExtractor.
//
// License note: PDF extraction uses github.com/ledongthuc/pdf (BSD-3-Clause),
// HTML uses golang.org/x/net/html (BSD-3-Clause) — both permissive, consistent
// with the repo-wide MIT/Apache/BSD-only policy. It intentionally does NOT use
// unidoc/unipdf (AGPL/commercial).
package ingest

import (
	"context"
	"errors"
	"strings"
)

// Common MIME content types this package recognizes. Values are the bare media
// type (no parameters like "; charset=utf-8"); Normalize strips parameters
// before comparison.
const (
	ContentTypePDF   = "application/pdf"
	ContentTypeHTML  = "text/html"
	ContentTypePlain = "text/plain"
)

// ErrEmptyContent is returned when extraction yields no usable text — e.g. a
// scanned/image-only PDF that carries no embedded text layer (OCR, which would
// recover such text, is intentionally out of scope for DefaultExtractor). It is
// a sentinel so callers can distinguish "the file had no text" from a parse
// failure and decide whether to skip, warn, or route to an OCR extractor.
var ErrEmptyContent = errors.New("ingest: no extractable text (an image-only/scanned document needs OCR, which is out of scope)")

// Extractor turns raw document bytes into plain UTF-8 text. It is the swappable
// seam a consumer overrides via aisotropy.WithExtractor to bring their own
// parser (OCR, DOCX, a remote extraction service). Implementations MUST be
// tenant-agnostic: they see opaque bytes and a MIME hint, never a scope.
type Extractor interface {
	// Extract returns the plain-text body of data. contentType is a MIME hint
	// (e.g. "application/pdf"); an empty contentType means "sniff it from the
	// bytes" (see DetectContentType). Implementations should honor ctx
	// cancellation for expensive extraction. A document that parses but carries
	// no text should return ErrEmptyContent rather than an empty string, so the
	// caller never silently ingests nothing.
	Extract(ctx context.Context, data []byte, contentType string) (string, error)
}

// Normalize lowercases a MIME type and strips any parameters (everything from
// the first ';'), so "TEXT/HTML; charset=utf-8" compares equal to "text/html".
func Normalize(contentType string) string {
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// DetectContentType resolves the effective content type for data. A non-empty
// declared type wins (normalized); otherwise the bytes are sniffed: a "%PDF-"
// magic prefix => application/pdf, an HTML doctype/<html> lead => text/html,
// everything else => text/plain. Sniffing is best-effort and only used when the
// caller did not declare a type.
func DetectContentType(data []byte, declared string) string {
	if n := Normalize(declared); n != "" {
		return n
	}
	if hasPDFMagic(data) {
		return ContentTypePDF
	}
	if looksLikeHTML(data) {
		return ContentTypeHTML
	}
	return ContentTypePlain
}

func hasPDFMagic(data []byte) bool {
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

func looksLikeHTML(data []byte) bool {
	// Sniff a small, case-insensitive window for an HTML lead token.
	end := len(data)
	if end > 512 {
		end = 512
	}
	head := strings.ToLower(string(data[:end]))
	head = strings.TrimLeft(head, " \t\r\n\ufeff")
	return strings.HasPrefix(head, "<!doctype html") ||
		strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<head") ||
		strings.Contains(head, "<body")
}

// DefaultExtractor returns the built-in Extractor: PDF via github.com/ledongthuc/pdf,
// HTML via golang.org/x/net/html, and plain text passed through unchanged.
// Unknown content types are treated as plain text (bytes decoded as UTF-8),
// which is a safe superset for the common "text file with an unusual MIME type"
// case. It is stateless and safe for concurrent use.
func DefaultExtractor() Extractor { return defaultExtractor{} }

type defaultExtractor struct{}

func (defaultExtractor) Extract(ctx context.Context, data []byte, contentType string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch DetectContentType(data, contentType) {
	case ContentTypePDF:
		return ExtractPDF(data)
	case ContentTypeHTML:
		return ExtractHTML(data)
	default:
		text := strings.TrimSpace(string(data))
		if text == "" {
			return "", ErrEmptyContent
		}
		return text, nil
	}
}
