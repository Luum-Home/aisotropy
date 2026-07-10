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
