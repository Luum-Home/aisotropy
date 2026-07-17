package ingest

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ExtractHTML returns the visible text of an HTML document, using
// golang.org/x/net/html (BSD-3-Clause). Script, style, and other non-rendered
// element contents are dropped; block-level boundaries become spaces so words
// don't run together. It is intentionally lightweight — a readability/main-content
// heuristic is out of scope; a consumer who needs one can plug their own
// Extractor via aisotropy.WithExtractor.
func ExtractHTML(data []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("ingest: parse html: %w", err)
	}

	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Noscript, atom.Template:
				return // skip non-rendered subtrees entirely
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t)
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	text := strings.TrimSpace(collapseWhitespace(b.String()))
	if text == "" {
		return "", ErrEmptyContent
	}
	return text, nil
}

// collapseWhitespace squeezes runs of whitespace (including newlines) into
// single spaces, shared by the PDF and HTML extractors to normalize the noisy
// whitespace those formats emit before the text hits the chunker.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
