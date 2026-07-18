package ingesttest

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

// TestMinimalPDF_IsWellFormed checks the byte-level structural shape
// MinimalPDF promises: a PDF-1.4 header, five well-formed indirect objects
// (Catalog/Pages/Page/Contents/Font), an xref table sized for those five
// objects plus the free-list head, a trailer naming the Catalog as /Root,
// and the %%EOF marker — the exact shape github.com/ledongthuc/pdf (the
// library internal/ingest.ExtractPDF is built on) requires to parse at all.
func TestMinimalPDF_IsWellFormed(t *testing.T) {
	got := MinimalPDF("structural check")
	s := string(got)

	if !strings.HasPrefix(s, "%PDF-1.4\n") {
		t.Errorf("MinimalPDF output does not start with the PDF-1.4 header:\n%s", s)
	}
	if !strings.HasSuffix(s, "%%EOF") {
		t.Errorf("MinimalPDF output does not end with %%%%EOF:\n%s", s)
	}
	if got := strings.Count(s, "endobj"); got != 5 {
		t.Errorf("MinimalPDF output has %d \"endobj\" markers, want 5 (objects 1-5)", got)
	}
	if !strings.Contains(s, "xref\n0 6\n") {
		t.Errorf("MinimalPDF output missing the expected xref subsection header \"xref\\n0 6\\n\":\n%s", s)
	}
	if !strings.Contains(s, "trailer\n<< /Size 6 /Root 1 0 R >>") {
		t.Errorf("MinimalPDF output missing the expected trailer:\n%s", s)
	}
	for n := 1; n <= 5; n++ {
		want := strconv.Itoa(n) + " 0 obj\n"
		if !strings.Contains(s, want) {
			t.Errorf("MinimalPDF output missing object header %q", want)
		}
	}
}

// TestMinimalPDF_ParsesWithRealPDFLibrary is the acceptance-level check: the
// bytes MinimalPDF produces must actually be readable by
// github.com/ledongthuc/pdf — the same library internal/ingest.ExtractPDF
// depends on — and the exact text passed to MinimalPDF must round-trip
// through the extracted page content.
func TestMinimalPDF_ParsesWithRealPDFLibrary(t *testing.T) {
	const want = "AISOTROPY INGESTTEST FIXTURE quokka"
	data := MinimalPDF(want)

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("pdf.NewReader: %v", err)
	}

	buf, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(buf); err != nil {
		t.Fatalf("reading extracted text: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, want) {
		t.Errorf("extracted text = %q, want it to contain %q", got, want)
	}
}

// TestMinimalPDF_DifferentTextProducesDifferentBytes is a light sanity check
// that the text argument actually flows into the content stream (and its
// Length is recomputed) rather than MinimalPDF ignoring its argument.
func TestMinimalPDF_DifferentTextProducesDifferentBytes(t *testing.T) {
	a := MinimalPDF("first marker aardvark")
	b := MinimalPDF("second marker zebra")
	if bytes.Equal(a, b) {
		t.Fatal("MinimalPDF(\"first marker aardvark\") == MinimalPDF(\"second marker zebra\"); expected the text argument to change the output")
	}
	if !bytes.Contains(a, []byte("first marker aardvark")) {
		t.Error("MinimalPDF output does not contain its own input text verbatim in the content stream")
	}
}

// TestMinimalPDF_EmptyTextStillParses covers the boundary of an empty
// content string: the content stream's Tj operand and /Length must still
// produce a structurally valid, parseable PDF.
func TestMinimalPDF_EmptyTextStillParses(t *testing.T) {
	data := MinimalPDF("")
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("pdf.NewReader on empty-text fixture: %v", err)
	}
	if r.NumPage() != 1 {
		t.Errorf("NumPage() = %d, want 1", r.NumPage())
	}
}
