package api

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// TestChatCheckQuota covers the pre-LLM quota gate's three branches: a nil
// limiter skips the check (nil error), an exhausted bucket returns a clean 429,
// and a limiter.Allow failure (a cancelled context) maps to a 500 — all BEFORE
// any token is spent.
func TestChatCheckQuota(t *testing.T) {
	t.Run("nil limiter skips", func(t *testing.T) {
		if err := chatCheckQuota(context.Background(), nil, "acme"); err != nil {
			t.Errorf("nil limiter must skip the check, got %v", err)
		}
	})

	t.Run("exhausted bucket is 429", func(t *testing.T) {
		wm := newTestWorkingMemory(t)
		limiter := wm.NewLimiter(0, 1) // capacity 1, no refill
		ctx := context.Background()
		// Drain the single token.
		if err := chatCheckQuota(ctx, limiter, "acme"); err != nil {
			t.Fatalf("first request should be allowed: %v", err)
		}
		err := chatCheckQuota(ctx, limiter, "acme")
		if err == nil {
			t.Fatal("second request over an exhausted bucket must be rejected")
		}
		if se, ok := err.(huma.StatusError); !ok || se.GetStatus() != 429 {
			t.Errorf("exhausted-bucket status = %v, want 429", err)
		}
	})

	t.Run("limiter error is 500", func(t *testing.T) {
		wm := newTestWorkingMemory(t)
		limiter := wm.NewLimiter(10, 10)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // a cancelled ctx makes limiter.Allow return an error
		err := chatCheckQuota(ctx, limiter, "acme")
		if err == nil {
			t.Fatal("a limiter failure must return an error")
		}
		if se, ok := err.(huma.StatusError); !ok || se.GetStatus() != 500 {
			t.Errorf("limiter-failure status = %v, want 500", err)
		}
	})
}

// TestDecodeChatReply covers the ValidateFunc used by the reflection fallback:
// valid JSON with a non-empty reply decodes; invalid JSON and a valid-but-empty
// reply are both schema violations.
func TestDecodeChatReply(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		out, err := decodeChatReply(`{"reply":"hello"}`)
		if err != nil {
			t.Fatalf("decodeChatReply: %v", err)
		}
		if out.Reply != "hello" {
			t.Errorf("reply = %q, want hello", out.Reply)
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		if _, err := decodeChatReply(`not json`); err == nil {
			t.Fatal("invalid JSON must be a schema violation")
		}
	})
	t.Run("empty reply", func(t *testing.T) {
		_, err := decodeChatReply(`{"reply":"   "}`)
		if err == nil {
			t.Fatal("a valid-but-empty reply must be a schema violation")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("error = %v, want it to mention the empty reply", err)
		}
	})
}

// TestValidateChatReply covers the business-validation step: a non-empty reply
// accepts (empty feedback), an empty one requests a correction (non-empty
// feedback, nil error).
func TestValidateChatReply(t *testing.T) {
	if fb, err := validateChatReply(context.Background(), chatStructuredReply{Reply: "ok"}); err != nil || fb != "" {
		t.Errorf("valid reply: feedback=%q err=%v, want (\"\", nil)", fb, err)
	}
	fb, err := validateChatReply(context.Background(), chatStructuredReply{Reply: "  "})
	if err != nil {
		t.Fatalf("validateChatReply must not error on an empty reply: %v", err)
	}
	if fb == "" {
		t.Error("an empty reply must request a correction (non-empty feedback)")
	}
}
