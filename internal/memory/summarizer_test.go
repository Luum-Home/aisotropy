package memory

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestNoopSummarizerStaysBoundedAcrossRounds is the regression test for the
// unbounded-growth defect measured in bench/token-economy: NoopSummarizer used
// to append one segment per condensation round without capping the total, so
// the summary grew without limit (~3.3k tokens over 50 turns). It must now stay
// within maxNoopSummaryLen no matter how many rounds run.
func TestNoopSummarizerStaysBoundedAcrossRounds(t *testing.T) {
	var s NoopSummarizer
	summary := ""
	oldest := []Message{
		{Role: "user", Content: strings.Repeat("a very long user message that exceeds the snippet cap ", 5)},
		{Role: "assistant", Content: strings.Repeat("an equally long assistant reply to be condensed ", 5)},
	}

	const rounds = 200
	maxSeen := 0
	for i := range rounds {
		next, err := s.Condense(context.Background(), summary, oldest)
		if err != nil {
			t.Fatalf("round %d: Condense returned error: %v", i, err)
		}
		if len(next) > maxNoopSummaryLen {
			t.Fatalf("round %d: summary length %d exceeds cap %d (unbounded growth regression)",
				i, len(next), maxNoopSummaryLen)
		}
		if !utf8.ValidString(next) {
			t.Fatalf("round %d: summary is not valid UTF-8 (rune split by the bound)", i)
		}
		if len(next) > maxSeen {
			maxSeen = len(next)
		}
		summary = next
	}

	// After enough rounds to force elision, the marker must be present — proving
	// the cap actually engaged rather than the input simply being small.
	if !strings.Contains(summary, strings.TrimSpace(noopElisionMarker)) {
		t.Fatalf("expected the elision marker once the cap engages; summary = %q", summary)
	}
	t.Logf("summary stayed bounded: max length %d over %d rounds (cap %d)", maxSeen, rounds, maxNoopSummaryLen)
}

// TestNoopSummarizerShortSummaryUnchanged verifies the cap does not touch a
// summary that fits the budget (no spurious elision marker, content preserved).
func TestNoopSummarizerShortSummaryUnchanged(t *testing.T) {
	var s NoopSummarizer
	out, err := s.Condense(context.Background(), "", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if strings.Contains(out, strings.TrimSpace(noopElisionMarker)) {
		t.Fatalf("short summary should not be elided, got %q", out)
	}
	if !strings.Contains(out, `user said "hi"`) {
		t.Fatalf("expected the condensed content, got %q", out)
	}
}
