package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// flakyClient is a Client whose StreamChat fails or succeeds on command.
type flakyClient struct {
	mu    sync.Mutex
	fail  bool
	calls int
}

func (f *flakyClient) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *flakyClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakyClient) Name() string { return "flaky" }

func (f *flakyClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	f.mu.Lock()
	f.calls++
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return nil, errors.New("provider down")
	}
	ch := make(chan Token, 1)
	ch <- Token{Done: true}
	close(ch)
	return ch, nil
}

// TestWithCircuitBreaker_DefaultsThresholdAndCooldown proves an invalid
// threshold (<1) and cooldown (<=0) fall back to the documented defaults (5
// and 30s) rather than propagating a nonsensical zero/negative breaker
// configuration.
func TestWithCircuitBreaker_DefaultsThresholdAndCooldown(t *testing.T) {
	fc := &flakyClient{}
	cb := WithCircuitBreaker(fc, 0, 0).(*circuitClient)
	if cb.threshold != 5 {
		t.Fatalf("threshold = %d, want default 5", cb.threshold)
	}
	if cb.cooldown != 30*time.Second {
		t.Fatalf("cooldown = %v, want default 30s", cb.cooldown)
	}

	fc2 := &flakyClient{}
	cb2 := WithCircuitBreaker(fc2, -3, -time.Second).(*circuitClient)
	if cb2.threshold != 5 {
		t.Fatalf("negative threshold = %d, want default 5", cb2.threshold)
	}
	if cb2.cooldown != 30*time.Second {
		t.Fatalf("negative cooldown = %v, want default 30s", cb2.cooldown)
	}
}

// TestCircuitToolCaller_FailsFastWhenOpen proves circuitToolCaller.CallTools
// fails fast with ErrCircuitOpen (without ever reaching the inner ToolCaller)
// once the shared breaker has tripped open — mirroring StreamChat's fail-fast
// behavior for the native tool-calling path.
func TestCircuitToolCaller_FailsFastWhenOpen(t *testing.T) {
	inner := &fakeToolCaller{Client: NewStubClient(), script: []ToolCallResult{{Message: "a"}}}
	wrapped := WithCircuitBreaker(inner, 1, time.Minute)
	tc, ok := wrapped.(ToolCaller)
	if !ok {
		t.Fatal("WithCircuitBreaker did not preserve the inner ToolCaller")
	}
	cb := wrapped.(*circuitToolCaller)
	base := time.Now()
	cb.now = func() time.Time { return base }

	// Trip the breaker directly via record (threshold=1): no real failing
	// CallTools call needed to set up the "open" precondition.
	cb.record(false)
	if cb.state != circuitOpen {
		t.Fatalf("breaker state = %v, want circuitOpen after one recorded failure at threshold 1", cb.state)
	}

	if _, err := tc.CallTools(context.Background(), "sys", "prompt", nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("CallTools while open = %v, want ErrCircuitOpen", err)
	}
	if inner.turn != 0 {
		t.Fatalf("inner ToolCaller was called (turn=%d) despite the breaker being open", inner.turn)
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	fc := &flakyClient{fail: true}
	cb := WithCircuitBreaker(fc, 3, time.Minute).(*circuitClient)
	base := time.Now()
	cb.now = func() time.Time { return base }
	ctx := context.Background()

	// Three consecutive failures trip the breaker.
	for i := range 3 {
		if _, err := cb.StreamChat(ctx, "", ""); err == nil {
			t.Fatalf("call %d: expected provider error", i)
		}
	}

	// Open: the next call fails fast without touching the inner client.
	before := fc.callCount()
	if _, err := cb.StreamChat(ctx, "", ""); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if fc.callCount() != before {
		t.Fatal("inner client was called while the breaker was open")
	}

	// After the cooldown the provider has recovered: the half-open trial closes
	// the breaker.
	cb.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	fc.setFail(false)
	if _, err := cb.StreamChat(ctx, "", ""); err != nil {
		t.Fatalf("half-open trial should succeed, got %v", err)
	}
	// Fully closed again: subsequent calls pass through.
	if _, err := cb.StreamChat(ctx, "", ""); err != nil {
		t.Fatalf("closed breaker should pass, got %v", err)
	}
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	fc := &flakyClient{fail: true}
	cb := WithCircuitBreaker(fc, 2, time.Minute).(*circuitClient)
	base := time.Now()
	cb.now = func() time.Time { return base }
	ctx := context.Background()

	for range 2 {
		_, _ = cb.StreamChat(ctx, "", "")
	}
	// Open. Cooldown elapses -> half-open trial, but provider still down -> the
	// single trial fails and re-opens immediately.
	cb.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if _, err := cb.StreamChat(ctx, "", ""); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("half-open should allow the trial through, not fail fast")
	}
	// Re-opened: the very next call fails fast again.
	if _, err := cb.StreamChat(ctx, "", ""); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("after a failed trial the breaker should re-open, got %v", err)
	}
}
