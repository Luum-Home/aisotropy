package llm

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by a circuit-broken client while the breaker is
// open: the upstream provider is failing, so calls fail fast instead of piling
// onto a struggling service (Phase 7 granular control, the resilience axis of
// docs/architecture/granular-control.md).
var ErrCircuitOpen = errors.New("llm: circuit breaker open (provider failing, backing off)")

type circuitState int

const (
	circuitClosed   circuitState = iota // normal: calls pass through
	circuitOpen                         // failing fast, waiting out the cooldown
	circuitHalfOpen                     // one trial call allowed to test recovery
)

// circuitClient decorates a Client with a circuit breaker keyed on StreamChat
// start failures (connection/auth/provider-down). After failureThreshold
// consecutive failures it opens for cooldown; the next call after cooldown is a
// half-open trial that closes the breaker on success or re-opens it on failure.
type circuitClient struct {
	inner     Client
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu       sync.Mutex
	state    circuitState
	failures int
	openedAt time.Time
}

// WithCircuitBreaker wraps c so repeated StreamChat start failures trip a
// breaker. threshold < 1 defaults to 5; cooldown <= 0 defaults to 30s.
//
// If c also implements ToolCaller (native tool-calling, ADR-0012), the returned
// value ALSO implements ToolCaller, gating CallTools on the SAME breaker — so
// the decorator never erases the capability (a decorator that dropped CallTools
// would make client.(ToolCaller) fail at the call site and silently disable the
// native tool-calling path). A non-ToolCaller c yields a plain breaker.
func WithCircuitBreaker(c Client, threshold int, cooldown time.Duration) Client {
	if threshold < 1 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	b := &circuitClient{inner: c, threshold: threshold, cooldown: cooldown, now: time.Now, state: circuitClosed}
	if tc, ok := c.(ToolCaller); ok {
		return &circuitToolCaller{circuitClient: b, tc: tc}
	}
	return b
}

// circuitToolCaller preserves an inner ToolCaller's native tool-calling through
// the breaker. It embeds *circuitClient (so Name/StreamChat and the breaker
// state are shared) and gates CallTools on that same breaker: a native
// tool-call is an upstream provider call too, so a failing provider trips the
// breaker no matter which call path reached it.
type circuitToolCaller struct {
	*circuitClient
	tc ToolCaller
}

var _ ToolCaller = (*circuitToolCaller)(nil)

// CallTools fails fast with ErrCircuitOpen while the breaker is open; otherwise
// it delegates to the inner ToolCaller and records the outcome on the shared
// breaker (like StreamChat).
func (b *circuitToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	if err := b.admit(); err != nil {
		return ToolCallResult{}, err
	}
	res, err := b.tc.CallTools(ctx, system, prompt, tools)
	b.record(err == nil)
	return res, err
}

// Name reports the wrapped client's identity unchanged.
func (b *circuitClient) Name() string { return b.inner.Name() }

// StreamChat fails fast with ErrCircuitOpen while the breaker is open; otherwise
// it calls the inner client and records the outcome (a non-nil start error is a
// failure; a successfully opened stream is a success).
func (b *circuitClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	if err := b.admit(); err != nil {
		return nil, err
	}
	ch, err := b.inner.StreamChat(ctx, system, prompt)
	b.record(err == nil)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// admit decides whether a call may proceed, transitioning open -> half-open once
// the cooldown elapses.
func (b *circuitClient) admit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == circuitOpen {
		if b.now().Sub(b.openedAt) < b.cooldown {
			return ErrCircuitOpen
		}
		b.state = circuitHalfOpen // allow a single trial
	}
	return nil
}

// record updates breaker state from a call outcome.
func (b *circuitClient) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		b.state = circuitClosed
		return
	}
	b.failures++
	// A failed trial in half-open, or crossing the threshold in closed, opens it.
	if b.state == circuitHalfOpen || b.failures >= b.threshold {
		b.state = circuitOpen
		b.openedAt = b.now()
	}
}
