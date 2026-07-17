package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// discardSink is a StreamSink that accumulates delivered tokens, for driving
// StreamCore directly without a wire encoder.
func discardSink(got *strings.Builder) StreamSink {
	return StreamSink{
		OnToken: func(text string) error { got.WriteString(text); return nil },
		OnDone:  func(StreamFinal) {},
	}
}

// TestStreamCore_QuotaExhaustedRejects covers the constitutional quota gate in
// StreamCore: with the tenant's bucket drained, the next stream is rejected
// BEFORE any LLM call (a rate-limit error), so a throttled tenant never spends
// tokens.
func TestStreamCore_QuotaExhaustedRejects(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(0, 1) // capacity 1, no refill
	deps := StreamDeps{Client: llm.NewStubClient(), WM: wm, Limiter: limiter}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var out strings.Builder
	// First stream drains the single token.
	if err := StreamCore(context.Background(), deps, in, discardSink(&out)); err != nil {
		t.Fatalf("first stream should be allowed: %v", err)
	}
	// Second stream is over quota.
	err := StreamCore(context.Background(), deps, in, discardSink(&out))
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("second stream error = %v, want a rate-limit rejection", err)
	}
}

// TestStreamCore_InvalidIdentityRejected covers the identity-validation guard:
// an empty tenant fails withIdentity before any store or LLM is touched.
func TestStreamCore_InvalidIdentityRejected(t *testing.T) {
	deps := StreamDeps{Client: llm.NewStubClient(), WM: newTestWorkingMemory(t)}
	in := &ChatInput{TenantID: "", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var out strings.Builder
	if err := StreamCore(context.Background(), deps, in, discardSink(&out)); err == nil {
		t.Fatal("StreamCore with an empty tenant must reject the request")
	}
}

// TestStreamCore_HappyPathDeliversTokensAndDone drives the full pipeline through
// the sink over the stub client, asserting tokens are delivered and OnDone fires
// with the tenant echo — the sink-side equivalent of the SSE happy path.
func TestStreamCore_HappyPathDeliversTokensAndDone(t *testing.T) {
	deps := StreamDeps{Client: llm.NewStubClient(), WM: newTestWorkingMemory(t)}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hello", SessionID: "s1"}}

	var got strings.Builder
	var doneTenant string
	var doneFired bool
	sink := StreamSink{
		OnToken: func(text string) error { got.WriteString(text); return nil },
		OnDone: func(f StreamFinal) {
			doneFired = true
			doneTenant = f.TenantID
		},
	}
	if err := StreamCore(context.Background(), deps, in, sink); err != nil {
		t.Fatalf("StreamCore: %v", err)
	}
	if !doneFired {
		t.Error("OnDone was never called on a clean end-of-stream")
	}
	if doneTenant != "acme" {
		t.Errorf("done tenant = %q, want acme", doneTenant)
	}
	if got.Len() == 0 {
		t.Error("no assistant tokens were delivered")
	}
}

// tokenClient streams a single fixed token, so a test can tell which client
// served a turn by the delivered text.
type tokenClient struct{ name, token string }

func (c tokenClient) Name() string { return c.name }

func (c tokenClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: c.token}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

// TestStreamCore_DegradesToCheapUnderQuotaPressure covers the quota-pressure
// degradation branch: with a cheap client configured and the tenant's bucket
// nearly drained (pressure >= the degrade threshold) but a token still available
// (so the hard quota gate passes), StreamCore routes to the cheap model instead
// of a hard 429 — graceful degradation. The cheap client's distinctive token
// proves it served the turn.
func TestStreamCore_DegradesToCheapUnderQuotaPressure(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(0, 100) // capacity 100, no refill: pressure is deterministic
	ctx := context.Background()
	// Drain 96 of 100 tokens so the bucket is at 96% pressure before StreamCore
	// (which then deducts one more, pushing pressure past the 0.95 threshold while
	// leaving tokens available so Allow still passes).
	for i := 0; i < 96; i++ {
		if ok, err := limiter.Allow(ctx, "acme"); err != nil || !ok {
			t.Fatalf("drain Allow %d = (%v,%v)", i, ok, err)
		}
	}

	deps := StreamDeps{
		Client:      tokenClient{name: "primary", token: "PRIMARY"},
		CheapClient: tokenClient{name: "cheap", token: "CHEAP"},
		WM:          wm,
		Limiter:     limiter,
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var got strings.Builder
	if err := StreamCore(ctx, deps, in, discardSink(&got)); err != nil {
		t.Fatalf("StreamCore under pressure should degrade, not fail: %v", err)
	}
	if !strings.Contains(got.String(), "CHEAP") {
		t.Errorf("delivered %q, want the CHEAP model's output (degradation under quota pressure)", got.String())
	}
}
