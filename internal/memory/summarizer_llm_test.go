package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// scriptedClient is an llm.Client double: StreamChat either fails up front
// (openErr) or streams the scripted tokens, then closes. It records the
// system/prompt it was handed so the transcript assembly can be asserted.
type scriptedClient struct {
	openErr    error
	tokens     []llm.Token
	gotSystem  string
	gotPrompt  string
	closeAfter bool // if true, close the channel without a Done token
}

func (c *scriptedClient) Name() string { return "scripted" }

func (c *scriptedClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	c.gotSystem = system
	c.gotPrompt = prompt
	if c.openErr != nil {
		return nil, c.openErr
	}
	ch := make(chan llm.Token, len(c.tokens)+1)
	go func() {
		defer close(ch)
		for _, tok := range c.tokens {
			select {
			case <-ctx.Done():
				return
			case ch <- tok:
			}
		}
	}()
	return ch, nil
}

func TestLLMSummarizerCondenseSuccess(t *testing.T) {
	client := &scriptedClient{tokens: []llm.Token{
		{Text: "condensed "},
		{Text: "session state"},
		{Done: true},
	}}
	s := LLMSummarizer{Client: client}
	out, err := s.Condense(context.Background(), "prior summary",
		[]Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}})
	if err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if out != "condensed session state" {
		t.Fatalf("summary = %q, want %q", out, "condensed session state")
	}
	// The transcript must carry both the previous summary and the messages.
	if !strings.Contains(client.gotPrompt, "Previous summary: prior summary") {
		t.Fatalf("prompt missing previous summary: %q", client.gotPrompt)
	}
	if !strings.Contains(client.gotPrompt, "user: hello") || !strings.Contains(client.gotPrompt, "assistant: hi") {
		t.Fatalf("prompt missing messages: %q", client.gotPrompt)
	}
	if client.gotSystem != summarizerSystemPrompt {
		t.Fatalf("system prompt = %q, want the condensation system prompt", client.gotSystem)
	}
}

// TestLLMSummarizerCondenseChannelCloseTerminates verifies the stream ending
// by a plain channel close (no explicit Done token) still returns the buffered
// text — the "!ok" branch of the select loop.
func TestLLMSummarizerCondenseChannelCloseTerminates(t *testing.T) {
	client := &scriptedClient{tokens: []llm.Token{{Text: "partial state"}}}
	s := LLMSummarizer{Client: client}
	out, err := s.Condense(context.Background(), "", []Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if out != "partial state" {
		t.Fatalf("summary = %q, want %q", out, "partial state")
	}
}

func TestLLMSummarizerCondenseEmptyOldestShortCircuits(t *testing.T) {
	client := &scriptedClient{openErr: errors.New("must not be called")}
	s := LLMSummarizer{Client: client}
	out, err := s.Condense(context.Background(), "unchanged", nil)
	if err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if out != "unchanged" {
		t.Fatalf("summary = %q, want the existing summary unchanged", out)
	}
	if client.gotPrompt != "" {
		t.Fatal("StreamChat was called for an empty message batch")
	}
}

func TestLLMSummarizerCondenseStreamOpenError(t *testing.T) {
	want := errors.New("provider unreachable")
	s := LLMSummarizer{Client: &scriptedClient{openErr: want}}
	if _, err := s.Condense(context.Background(), "", []Message{{Role: "user", Content: "x"}}); err == nil || !errors.Is(err, want) {
		t.Fatalf("Condense error = %v, want wrap of %v", err, want)
	}
}

func TestLLMSummarizerCondenseContextCancelled(t *testing.T) {
	// A client that never sends a token, so the loop blocks until ctx cancel.
	client := &scriptedClient{tokens: []llm.Token{{Text: "never delivered"}}}
	s := LLMSummarizer{Client: client}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the select's ctx.Done() branch wins
	if _, err := s.Condense(ctx, "", []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected a context-cancelled error")
	}
}

// TestWorkingMemoryUsesLLMSummarizerEndToEnd wires an LLMSummarizer into the
// WorkingMemory condensation path so a condensation round actually runs the
// scripted client and prunes the active buffer.
func TestWorkingMemoryUsesLLMSummarizerEndToEnd(t *testing.T) {
	client := &scriptedClient{tokens: []llm.Token{{Text: "distilled"}, {Done: true}}}
	wm, err := OpenInMemory(2, LLMSummarizer{Client: client})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	for i := 0; i < 6; i++ {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: "msg"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Wait for the background condensation to write a summary.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sum, err := wm.Summary(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sum, "distilled") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("LLMSummarizer condensation never produced a summary")
}
