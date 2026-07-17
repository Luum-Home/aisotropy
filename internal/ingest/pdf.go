package ingest

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ExtractPDF returns the concatenated plain text of every page in a PDF
// document, using github.com/ledongthuc/pdf (BSD-3-Clause). It extracts the
// embedded text layer only: a scanned/image-only PDF has no such layer and
// yields ErrEmptyContent — recovering that text would require OCR, which is
// deliberately out of scope for the built-in extractor (a consumer can plug an
// OCR-backed Extractor via aisotropy.WithExtractor).
//
// The underlying library can panic on a malformed content stream; per the
// project's Rust-boundary discipline of never letting a dependency crash the
// core, that panic is recovered and returned as an ordinary error.
func ExtractPDF(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("ingest: pdf extraction panicked (malformed or unsupported PDF): %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("ingest: open pdf: %w", err)
	}

	buf, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("ingest: extract pdf text: %w", err)
	}

	var out bytes.Buffer
	if _, err := io.Copy(&out, buf); err != nil {
		return "", fmt.Errorf("ingest: read pdf text: %w", err)
	}

	text = strings.TrimSpace(collapseWhitespace(out.String()))
	if text == "" {
		return "", ErrEmptyContent
	}
	return text, nil
}
