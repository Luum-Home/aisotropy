package memory

import (
	"context"
	"fmt"
	"strings"

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
// NoopSummarizer's heuristic summary, keeping the summary itself bounded in
// size across many condensation rounds.
const maxNoopSnippetLen = 80

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
	return sb.String(), nil
}

// summarizerSystemPrompt instructs the LLM to produce a single condensed
// paragraph rather than a structured or multi-part response, since the
// summary buffer is meant to be a compact drop-in replacement for the
// evicted messages inside the prompt window.
const summarizerSystemPrompt = "You are a memory-condensation assistant for a chat agent. " +
	"Given the current session-state summary (which may be empty) and a batch " +
	"of older chat messages being evicted from the active context window, " +
	"produce a single, concise paragraph that preserves the important facts, " +
	"decisions, and open threads. Respond with exactly one paragraph of plain " +
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
