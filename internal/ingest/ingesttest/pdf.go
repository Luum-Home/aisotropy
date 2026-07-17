// Package ingesttest provides test-only fixtures for the ingest package and its
// consumers (the aisotropy façade's IngestFile tests). It lives outside a
// _test.go file so the generated PDF fixture is reusable across packages
// without committing a binary blob to the repo.
package ingesttest

import (
	"bytes"
	"fmt"
	"strconv"
)

// MinimalPDF assembles a valid single-page PDF whose content stream shows text
// with the standard Helvetica font, computing xref byte offsets at build time
// so github.com/ledongthuc/pdf can parse and extract it. The returned bytes are
// a complete, spec-shaped PDF-1.4 document — no external tooling required.
func MinimalPDF(text string) []byte {
	var buf bytes.Buffer
	offsets := make([]int, 6) // objects 1..5 (index 0 unused)
	w := func(s string) { buf.WriteString(s) }

	w("%PDF-1.4\n")
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		w(strconv.Itoa(n) + " 0 obj\n" + body + "\nendobj\n")
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	content := "BT /F1 24 Tf 72 700 Td (" + text + ") Tj ET"
	obj(4, "<< /Length "+strconv.Itoa(len(content))+" >>\nstream\n"+content+"\nendstream")
	obj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOff := buf.Len()
	w("xref\n0 6\n")
	w("0000000000 65535 f \n")
	for n := 1; n <= 5; n++ {
		w(fmt.Sprintf("%010d 00000 n \n", offsets[n]))
	}
	w("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xrefOff) + "\n%%EOF")
	return buf.Bytes()
}
