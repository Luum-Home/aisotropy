package memory

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// Summarizer condenses a batch of messages being evicted from the active
// buffer into an updated "session state" paragraph (see
// docs/architecture/memory-tiers.md -> "Token optimization — Summary
// Buffer"). It is pluggable so WorkingMemory can run against a real LLM in
// production while tests and benchmarks use a deterministic, credential-free
// implementation.
type Summarizer interface {
	// Condense receives the current summary (empty on the first
	// condensation for a session) and the oldest messages being dropped
	// from the active buffer, in chronological order, and returns the new
	// summary text.
	Condense(ctx context.Context, existingSummary string, oldest []Message) (string, error)
}

// maxNoopSnippetLen truncates each condensed message's content in
// NoopSummarizer's heuristic summary. This bounds each round's contribution,
// but on its own does NOT bound the summary across rounds — see
// maxNoopSummaryLen.
const maxNoopSnippetLen = 80

// maxNoopSummaryLen bounds the TOTAL heuristic summary across many
// condensation rounds. NoopSummarizer appends one segment per round, so
// without this cap the summary grows unbounded (measured at ~3.3k tokens over
// 50 turns in bench/token-economy), defeating the summary buffer's whole
// purpose of a bounded prompt footprint. When the running summary would exceed
// this budget the oldest text is elided, keeping the most-recent session state
// (what matters most for the next turn) — the deterministic stand-in for a real
// LLMSummarizer re-condensing to a fixed size.
const maxNoopSummaryLen = 1024

// noopElisionMarker prefixes a summary whose older head was dropped to stay
// within maxNoopSummaryLen, so the elision is explicit rather than a silent
// truncation.
const noopElisionMarker = "[...older session state elided...] "

// NoopSummarizer is a deterministic, heuristic Summarizer that requires no
// LLM credentials: it appends a short, truncated description of each
// evicted message to the running summary text. It exists so
// WorkingMemory's summary-buffer behavior (a bounded active window) can be
// exercised and tested end-to-end without an API key — the same role
// internal/llm.StubClient plays for the LLM streaming path.
type NoopSummarizer struct{}

// Condense implements Summarizer.
func (NoopSummarizer) Condense(_ context.Context, existingSummary string, oldest []Message) (string, error) {
	if len(oldest) == 0 {
		return existingSummary, nil
	}

	var sb strings.Builder
	if existingSummary != "" {
		sb.WriteString(existingSummary)
		sb.WriteString(" ")
	}
	fmt.Fprintf(&sb, "Session state after condensing %d earlier message(s): ", len(oldest))
	for i, m := range oldest {
		if i > 0 {
			sb.WriteString("; ")
		}
		content := m.Content
		if len(content) > maxNoopSnippetLen {
			content = content[:maxNoopSnippetLen] + "..."
		}
		fmt.Fprintf(&sb, "%s said %q", m.Role, content)
	}
	sb.WriteString(".")
	return boundNoopSummary(sb.String()), nil
}

// boundNoopSummary caps the heuristic summary at maxNoopSummaryLen, keeping the
// most-recent tail and prefixing an explicit elision marker when older text is
// dropped. The tail is advanced to the next full rune so a multi-byte character
// is never split.
func boundNoopSummary(s string) string {
	if len(s) <= maxNoopSummaryLen {
		return s
	}
	keep := maxNoopSummaryLen - len(noopElisionMarker)
	if keep <= 0 {
		return s[len(s)-maxNoopSummaryLen:]
	}
	tail := s[len(s)-keep:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return noopElisionMarker + tail
}

// summarizerSystemPrompt instructs the LLM to produce a single condensed
// paragraph rather than a structured or multi-part response, since the
// summary buffer is meant to be a compact drop-in replacement for the
// evicted messages inside the prompt window.
//
// The prompt carries TWO explicit loss contracts, because condenseAndPrune
// folds the evicted messages into this paragraph IRREVERSIBLY — the originals
// are deleted in the same transaction, so anything the paragraph omits or gets
// wrong is permanently wrong for that session. Both failures below are the same
// underlying defect (lossy condensation with no instruction about what is
// irreplaceable) and both were observed in the arena's own store; see
// docs/architecture/arena-memory-forensics.md §4.
//
//  1. SUPERSESSION CONTRACT — the original prompt said only "preserve the
//     important facts", with no rule for a later message OVERRIDING an earlier
//     one. Observed: a summary asserting "seat 14C" for a conversation where the
//     seat had since changed 14C -> 22F -> 9A.
//  2. UNIQUENESS CONTRACT — nor did it say what may not be paraphrased away.
//     Observed: the needle "Vermillion Bravo", stated once at turn 1, condensed
//     into "a relaxed, rail-based coastal itinerary" — a well-formed summary of
//     the gist that silently dropped the one irreplaceable token. Generic
//     narrative is reconstructible from what remains; a unique identifier is not,
//     so the two are given explicitly different compression licenses.
//
// Both clauses are deliberately ONE short sentence each: this prompt is re-sent
// on every condensation, which is a real billable model call (see
// LLMSummarizer.Condense), so every added token is paid repeatedly.
// TestSummarizerPromptStaysShort holds that budget.
const summarizerSystemPrompt = "You are a memory-condensation assistant for a chat agent. " +
	"Given the current session-state summary (which may be empty) and a batch " +
	"of older chat messages being evicted from the active context window, " +
	"produce a single, concise paragraph that preserves the important facts, " +
	"decisions, and open threads. When a later message changes or replaces an " +
	"earlier fact, record ONLY the latest value and state it as the current " +
	"one; never carry a superseded value forward as if it were still true. " +
	"Carry over VERBATIM every value that could not be reconstructed from the " +
	"rest of the conversation — proper nouns, codenames, codes, combinations, " +
	"account/seat/reference numbers, dates, exact quantities — even when stated " +
	"only once and seemingly incidental; compress narrative freely, never a " +
	"unique identifier. " +
	"Respond with exactly one paragraph of plain " +
	"text — no headers, no bullet points, no preamble."

// LLMSummarizer is a Summarizer backed by a real llm.Client (see
// llm.FromEnv()): it asks the model to condense the previous summary plus
// the evicted messages into a single paragraph. Production code should use
// this implementation; tests and benchmarks should use NoopSummarizer so
// they don't depend on ANTHROPIC_API_KEY/OPENAI_API_KEY/OPENROUTER_API_KEY.
type LLMSummarizer struct {
	Client llm.Client
}

// Condense implements Summarizer by streaming a condensation request
// through the underlying llm.Client and buffering the full response (the
// Client interface is streaming-only; the summary is small so buffering it
// here is not a latency concern — see internal/llm/llm.go).
func (s LLMSummarizer) Condense(ctx context.Context, existingSummary string, oldest []Message) (string, error) {
	if len(oldest) == 0 {
		return existingSummary, nil
	}

	var transcript strings.Builder
	if existingSummary != "" {
		transcript.WriteString("Previous summary: ")
		transcript.WriteString(existingSummary)
		transcript.WriteString("\n\n")
	}
	transcript.WriteString("Messages to condense:\n")
	for _, m := range oldest {
		fmt.Fprintf(&transcript, "%s: %s\n", m.Role, m.Content)
	}

	tokens, err := s.Client.StreamChat(ctx, summarizerSystemPrompt, transcript.String())
	if err != nil {
		return "", fmt.Errorf("memory: llm summarizer stream: %w", err)
	}

	var out strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case tok, ok := <-tokens:
			if !ok {
				return strings.TrimSpace(out.String()), nil
			}
			if tok.Done {
				return strings.TrimSpace(out.String()), nil
			}
			out.WriteString(tok.Text)
		}
	}
}
