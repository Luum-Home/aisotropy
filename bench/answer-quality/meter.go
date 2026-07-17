package main

import (
	"context"
	"sync"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// meterClient is a transparent llm.Client decorator that accumulates the
// provider-reported token Usage of every completed stream. It is how this
// harness measures TOKEN SPEND without touching the App's internal cost meter
// or its wiring (aisotropy/app.go is out of scope): the eval builds the real
// provider client itself (llm.FromEnv — provider-agnostic), wraps it in a
// meterClient, and hands that to aisotropy.WithLLM. Because Usage rides the
// TERMINAL Token of a stream (see llm.Token.Usage), the decorator reads it as it
// passes through, adding zero latency and never buffering the response.
//
// The same type meters the judge client, so a run reports serving-side and
// judge-side spend separately and a grand total — the honest full cost of the
// eval, which is what bounds it.
type meterClient struct {
	inner llm.Client

	mu      sync.Mutex
	inTok   int64
	outTok  int64
	streams int64 // streams that reported a Usage summary
}

// newMeterClient wraps inner. A nil inner is a programming error (the caller
// always resolves a real-or-stub client first), so it is returned as-is and
// would panic on use — deliberately loud rather than silently swallowing spend.
func newMeterClient(inner llm.Client) *meterClient {
	return &meterClient{inner: inner}
}

// Name reports the wrapped client's name unchanged (observability sees the real
// provider, not the meter).
func (m *meterClient) Name() string { return m.inner.Name() }

// StreamChat opens the inner stream and re-emits every Token unchanged, reading
// Usage off the terminal Token(s) as they pass. It preserves streaming
// semantics exactly: the returned channel closes when the inner one does, and
// ctx cancellation still cuts the upstream stream (the only spend-cutoff
// mechanism, per llm.Client).
func (m *meterClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	in, err := m.inner.StreamChat(ctx, system, prompt)
	if err != nil {
		return in, err
	}
	out := make(chan llm.Token)
	go func() {
		defer close(out)
		for tok := range in {
			if tok.Usage != nil {
				m.mu.Lock()
				m.inTok += int64(tok.Usage.InputTokens)
				m.outTok += int64(tok.Usage.OutputTokens)
				m.streams++
				m.mu.Unlock()
			}
			select {
			case out <- tok:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// snapshot returns the cumulative input/output token totals and the number of
// streams that reported usage, as of now. Safe for concurrent use.
func (m *meterClient) snapshot() (inTok, outTok, streams int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inTok, m.outTok, m.streams
}
