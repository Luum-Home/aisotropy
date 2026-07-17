package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/ingest/ingesttest"
)

func TestExtractPDF_RoundTrip(t *testing.T) {
	const want = "AISOTROPY PDF INGEST FIXTURE aardvark"
	got, err := ExtractPDF(ingesttest.MinimalPDF(want))
	if err != nil {
		t.Fatalf("ExtractPDF: %v", err)
	}
	if !strings.Contains(got, want) {
		t.Fatalf("ExtractPDF = %q, want it to contain %q", got, want)
	}
}

func TestExtractPDF_GarbageIsErrorNotPanic(t *testing.T) {
	// A non-PDF byte blob must return an error (recovered from any internal
	// panic), never crash the process.
	if _, err := ExtractPDF([]byte("this is definitely not a pdf")); err == nil {
		t.Fatal("ExtractPDF on garbage bytes must return an error")
	}
}

func TestExtractHTML_StripsTagsAndScripts(t *testing.T) {
	html := []byte(`<!doctype html><html><head><title>t</title>
		<style>.x{color:red}</style></head>
		<body><h1>Onboarding</h1><script>var x=1;</script>
		<p>memory tiers and quotas</p></body></html>`)
	got, err := ExtractHTML(html)
	if err != nil {
		t.Fatalf("ExtractHTML: %v", err)
	}
	if !strings.Contains(got, "Onboarding") || !strings.Contains(got, "memory tiers and quotas") {
		t.Fatalf("ExtractHTML = %q, missing visible text", got)
	}
	if strings.Contains(got, "var x=1") || strings.Contains(got, "color:red") {
		t.Fatalf("ExtractHTML = %q leaked script/style content", got)
	}
}

func TestDetectContentType(t *testing.T) {
	cases := []struct {
		name     string
		data     []byte
		declared string
		want     string
	}{
		{"declared wins", []byte("%PDF-1.4"), "text/plain; charset=utf-8", ContentTypePlain},
		{"sniff pdf", ingesttest.MinimalPDF("x"), "", ContentTypePDF},
		{"sniff html doctype", []byte("<!DOCTYPE html><html></html>"), "", ContentTypeHTML},
		{"sniff plain", []byte("just some words"), "", ContentTypePlain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectContentType(tc.data, tc.declared); got != tc.want {
				t.Fatalf("DetectContentType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultExtractor_Dispatch(t *testing.T) {
	ex := DefaultExtractor()
	ctx := context.Background()

	pdfText, err := ex.Extract(ctx, ingesttest.MinimalPDF("hello pdf"), ContentTypePDF)
	if err != nil || !strings.Contains(pdfText, "hello pdf") {
		t.Fatalf("PDF dispatch: got %q err %v", pdfText, err)
	}
	txt, err := ex.Extract(ctx, []byte("  plain body  "), "")
	if err != nil || txt != "plain body" {
		t.Fatalf("plain dispatch: got %q err %v", txt, err)
	}
}

func TestDefaultExtractor_EmptyIsErrEmptyContent(t *testing.T) {
	_, err := DefaultExtractor().Extract(context.Background(), []byte("   "), ContentTypePlain)
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("empty plain input: err = %v, want ErrEmptyContent", err)
	}
}
