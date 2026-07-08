package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StubClient is a canned Client: it does not call any real LLM, it emits a
// word-by-word token stream deterministically derived from the received
// prompt. It exists so the full SSE pipeline (HTTP handler -> token channel
// -> per-event flush -> ctx cancellation) can be verified without needing
// ANTHROPIC_API_KEY — it is the path exercised in this development/CI
// environment.
type StubClient struct {
	// Delay is the pause between emitted tokens (simulates real streaming
	// latency). Defaults to 15ms.
	Delay time.Duration
}

// NewStubClient creates a StubClient with the default delay.
func NewStubClient() *StubClient {
	return &StubClient{Delay: 15 * time.Millisecond}
}

func (s *StubClient) Name() string { return "stub" }

// StreamChat emits, word by word, a canned response that echoes the received
// prompt. Respects ctx.Done(): if the client cancels midway, the producer
// goroutine stops immediately and closes the channel without emitting more
// tokens.
func (s *StubClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	delay := s.Delay
	if delay <= 0 {
		delay = 15 * time.Millisecond
	}

	words := buildStubWords(system, prompt)
	out := make(chan Token)

	go func() {
		defer close(out)

		for i, w := range words {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}

			select {
			case <-ctx.Done():
				return
			case out <- Token{Text: w}:
			}
		}

		select {
		case <-ctx.Done():
		case out <- Token{Done: true}:
		}
	}()

	return out, nil
}

// buildStubWords builds the canned response as a slice of "words" (each one
// includes the following space/punctuation so concatenating all the tokens
// reconstructs the exact text).
func buildStubWords(system, prompt string) []string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "(no prompt)"
	}
	reply := fmt.Sprintf(
		"stub-llm: received your message \"%s\" (system=%q). "+
			"This is a word-by-word echo to verify SSE streaming "+
			"end-to-end without needing ANTHROPIC_API_KEY.",
		prompt, system,
	)

	fields := strings.Fields(reply)
	words := make([]string, len(fields))
	for i, f := range fields {
		if i < len(fields)-1 {
			words[i] = f + " "
		} else {
			words[i] = f
		}
	}
	return words
}
