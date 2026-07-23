package llm

import (
	"context"
	"testing"
	"time"
)

// TestStubClient_CtxCancelDuringInterWordDelay proves the producer goroutine
// stops (and closes the channel) when ctx is cancelled while waiting between
// words — the `case <-ctx.Done(): return` branch inside the inter-word delay
// select, distinct from cancellation during the token send itself.
func TestStubClient_CtxCancelDuringInterWordDelay(t *testing.T) {
	c := &StubClient{Delay: time.Hour} // long enough that the test controls timing
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.StreamChat(ctx, "sys", "hello there stub")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	// Receive the first word (i==0, no delay wait), then cancel before the
	// second word's inter-word delay would elapse.
	first, ok := <-ch
	if !ok {
		t.Fatal("expected at least one token before cancellation")
	}
	if first.Text == "" {
		t.Fatalf("first token = %+v, want a non-empty word", first)
	}
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected no further tokens after cancellation mid-delay")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancellation (goroutine leak)")
	}
}

// TestStubClient_CtxCancelBeforeAnyToken proves cancelling ctx before the
// producer goroutine gets scheduled still closes the channel promptly rather
// than emitting the full canned reply — one of the send-side ctx.Done()
// branches (word token or terminal Done token) always wins within a bounded
// number of receives, since select's case is chosen pseudo-randomly whenever
// both the send and ctx.Done() are simultaneously ready against an active
// reader (Go has no priority between ready select cases), so an occasional
// single token before close is expected and not itself a bug.
func TestStubClient_CtxCancelBeforeAnyToken(t *testing.T) {
	c := &StubClient{Delay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	ch, err := c.StreamChat(ctx, "sys", "hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	deadline := time.After(2 * time.Second)
	received := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed: success, regardless of how many tokens preceded it
			}
			received++
			if received > 3 {
				t.Fatal("producer kept emitting well past cancellation instead of stopping promptly")
			}
		case <-deadline:
			t.Fatal("channel did not close for an already-cancelled ctx (goroutine leak)")
		}
	}
}

// TestStubStructuredReply_MalformedInputs covers stubStructuredReply's two
// "give up and fall back to free text" branches: a schema instruction with no
// trailing newline (so no schema text can be extracted), and a schema
// instruction whose embedded JSON does not parse.
func TestStubStructuredReply_MalformedInputs(t *testing.T) {
	t.Run("no newline after the marker", func(t *testing.T) {
		prompt := "please answer. " + schemaInstructionMarker // no '\n' after it
		if _, ok := stubStructuredReply("", prompt); ok {
			t.Fatal("expected ok=false when no newline follows the schema instruction marker")
		}
	})
	t.Run("embedded schema is not valid JSON", func(t *testing.T) {
		prompt := schemaInstructionMarker + "\nthis is not valid json{{{"
		if _, ok := stubStructuredReply("", prompt); ok {
			t.Fatal("expected ok=false for an unparseable embedded schema")
		}
	})
	t.Run("no marker present falls back cleanly", func(t *testing.T) {
		if _, ok := stubStructuredReply("", "just a plain prompt, no schema instruction"); ok {
			t.Fatal("expected ok=false when the prompt carries no schema instruction")
		}
	})
}
