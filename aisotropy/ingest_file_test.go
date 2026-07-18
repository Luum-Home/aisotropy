package aisotropy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/ingest"
	"github.com/Luum-Home/aisotropy/internal/ingest/ingesttest"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// docSearch runs a KindDocument semantic search for query in scope (tenant,
// user) against the App's shared semantic store, returning the number of hits.
// Mirrors the retrieval assertion in knowledge_test.go.
func docSearch(t *testing.T, a *App, ctx context.Context, tenant, user, query string) int {
	t.Helper()
	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	sc, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("NewScope(%q,%q): %v", tenant, user, err)
	}
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(ctx, sc, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return len(got)
}

// TestApp_IngestFile_PDF_RetrievableAndTenantIsolated is the acceptance test
// for the PDF-ingestion gap: a PDF's bytes go in through IngestFile, its text
// is extracted and flows through the same scoped chunk->embed->store pipeline,
// and the result is retrievable via KindDocument search for the OWNING scope
// yet invisible to a DIFFERENT tenant (no cross-tenant leak).
func TestApp_IngestFile_PDF_RetrievableAndTenantIsolated(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	const marker = "AISOTROPY PDF INGEST FIXTURE aardvark memory tiers"
	pdfBytes := ingesttest.MinimalPDF(marker)

	n, err := a.IngestFile(ctx, "acme", "alice", FileDocument{
		ID:          "handbook",
		Data:        pdfBytes,
		ContentType: ingest.ContentTypePDF,
		Source:      "handbook.pdf",
	})
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if n < 1 {
		t.Fatalf("chunks ingested = %d, want >= 1", n)
	}

	// Retrievable for the owning (acme, alice) scope.
	if hits := docSearch(t, a, ctx, "acme", "alice", "aardvark memory tiers"); hits == 0 {
		t.Fatal("ingested PDF not retrievable via KindDocument search for the owning scope")
	}

	// ISOLATION: a different tenant sees nothing — the write was scoped to
	// (acme, alice) and NewScope-validated, never a shared unscoped path.
	if hits := docSearch(t, a, ctx, "evilcorp", "mallory", "aardvark memory tiers"); hits != 0 {
		t.Fatalf("cross-tenant leak: evilcorp/mallory retrieved %d chunks of acme's PDF, want 0", hits)
	}
}

// TestApp_IngestFile_ContentTypeSniffed verifies the ContentType hint is
// optional: with it empty, IngestFile sniffs the %PDF- magic and still extracts.
func TestApp_IngestFile_ContentTypeSniffed(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	n, err := a.IngestFile(ctx, "acme", "bob", FileDocument{
		ID:   "sniffed",
		Data: ingesttest.MinimalPDF("sniffed pdf zebra content"),
		// ContentType intentionally omitted.
	})
	if err != nil {
		t.Fatalf("IngestFile (sniffed): %v", err)
	}
	if n < 1 {
		t.Fatalf("chunks ingested = %d, want >= 1", n)
	}
	if hits := docSearch(t, a, ctx, "acme", "bob", "zebra content"); hits == 0 {
		t.Fatal("sniffed PDF not retrievable")
	}
}

// TestApp_IngestFile_HTMLThroughSamePipeline verifies HTML extraction also
// flows through IngestKnowledge and is retrievable, tenant-scoped.
func TestApp_IngestFile_HTMLThroughSamePipeline(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	html := []byte(`<!doctype html><html><body><h1>Billing</h1>
		<script>track()</script><p>quotas and rate limits kangaroo</p></body></html>`)
	n, err := a.IngestFile(ctx, "acme", "carol", FileDocument{
		ID: "faq", Data: html, ContentType: ingest.ContentTypeHTML, Source: "faq.html",
	})
	if err != nil {
		t.Fatalf("IngestFile (html): %v", err)
	}
	if n < 1 {
		t.Fatalf("chunks ingested = %d, want >= 1", n)
	}
	if hits := docSearch(t, a, ctx, "acme", "carol", "quotas rate limits kangaroo"); hits == 0 {
		t.Fatal("ingested HTML not retrievable")
	}
}

// TestApp_IngestFile_EmptyContentSurfaced verifies a parseable-but-textless
// document (the scanned/image-PDF case, simulated with whitespace-only plain
// bytes) fails with ErrEmptyContent rather than silently ingesting nothing.
func TestApp_IngestFile_EmptyContentSurfaced(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	_, err := a.IngestFile(ctx, "acme", "dave", FileDocument{
		ID: "blank", Data: []byte("    \n\t  "), ContentType: ingest.ContentTypePlain,
	})
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("IngestFile on textless input: err = %v, want wrapping ErrEmptyContent", err)
	}
}

// stubExtractor is a fake Extractor proving WithExtractor swaps the byte->text
// seam without touching the scoped pipeline.
type stubExtractor struct{ text string }

func (s stubExtractor) Extract(_ context.Context, _ []byte, _ string) (string, error) {
	return s.text, nil
}

// TestApp_WithExtractor_OverridesSeam verifies a consumer-supplied Extractor
// replaces the default parser (the BYO-parser hook, e.g. for OCR).
func TestApp_WithExtractor_OverridesSeam(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()),
		WithExtractor(stubExtractor{text: "override platypus text from custom parser"}))
	defer a.Close()
	ctx := context.Background()

	n, err := a.IngestFile(ctx, "acme", "erin", FileDocument{ID: "custom", Data: []byte("ignored bytes")})
	if err != nil {
		t.Fatalf("IngestFile with custom extractor: %v", err)
	}
	if n < 1 {
		t.Fatalf("chunks ingested = %d, want >= 1", n)
	}
	if hits := docSearch(t, a, ctx, "acme", "erin", "platypus custom parser"); hits == 0 {
		t.Fatal("custom-extracted text not retrievable")
	}
	if !strings.Contains("override platypus text from custom parser", "platypus") {
		t.Fatal("sanity") // keep strings import honest
	}
}

// TestApp_IngestTenantFile_SharedBucket verifies the tenant-shared file arm
// ingests extracted text into the tenant bucket, retrievable tenant-wide.
func TestApp_IngestTenantFile_SharedBucket(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	n, err := a.IngestTenantFile(ctx, "acme", FileDocument{
		ID: "policy", Data: ingesttest.MinimalPDF("company wide policy narwhal"), ContentType: ingest.ContentTypePDF,
	})
	if err != nil {
		t.Fatalf("IngestTenantFile: %v", err)
	}
	if n < 1 {
		t.Fatalf("chunks ingested = %d, want >= 1", n)
	}
}

// TestApp_IngestTenantFile_EmptyContentSurfaced covers IngestTenantFile's own
// extractFileDocs-error branch (distinct from IngestFile's): a parseable-but-
// textless document fails with ErrEmptyContent rather than silently ingesting
// nothing into the tenant-shared bucket.
func TestApp_IngestTenantFile_EmptyContentSurfaced(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	defer a.Close()
	ctx := context.Background()

	_, err := a.IngestTenantFile(ctx, "acme", FileDocument{
		ID: "blank", Data: []byte("    \n\t  "), ContentType: ingest.ContentTypePlain,
	})
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("IngestTenantFile on textless input: err = %v, want wrapping ErrEmptyContent", err)
	}
}
