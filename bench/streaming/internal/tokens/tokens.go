// Package tokens is a deterministic token-rate stub shared by every transport
// server in the bake-off. It exists so the benchmark measures TRANSPORT cost,
// not model latency: every protocol emits the SAME number of tokens at the SAME
// wall-clock cadence, so any difference in TTFT / inter-token latency / CPU / mem
// is attributable to the wire protocol and its framing, not to the "model".
package tokens

import (
	"context"
	"strconv"
	"time"
)

// Stream describes a deterministic token stream.
type Stream struct {
	// Count is the total number of tokens to emit.
	Count int
	// Interval is the wall-clock delay between successive tokens. This models a
	// fixed decode rate (e.g. 20ms => 50 tok/s). The FIRST token is emitted
	// immediately (interval measures INTER-token latency, not prompt latency),
	// so the transport's own setup cost dominates TTFT.
	Interval time.Duration
}

// DefaultStream is the canonical stub used across the bake-off: 200 tokens at a
// 5ms cadence (~200 tok/s), long enough to observe sustained throughput and
// backpressure, short enough to run thousands of concurrent streams.
func DefaultStream() Stream { return Stream{Count: 200, Interval: 5 * time.Millisecond} }

// Token is one emitted unit: an index and a small deterministic payload.
type Token struct {
	Index int
	Text  string
}

// tokenText returns a deterministic, non-trivial payload (~a realistic BPE
// token width) so framing overhead is exercised rather than empty writes.
func tokenText(i int) string {
	return "tok-" + strconv.Itoa(i) + " "
}

// Emit drives the deterministic cadence, invoking fn for each token. It honors
// context cancellation immediately (cancellation-correctness gate, §7.3): a
// cancelled ctx stops emission at once and returns ctx.Err(). fn may return an
// error (e.g. a transport write failure / client gone) to stop early.
func (s Stream) Emit(ctx context.Context, fn func(Token) error) error {
	if s.Count <= 0 {
		return nil
	}
	// First token immediately — TTFT reflects transport setup, not decode delay.
	if err := fn(Token{Index: 0, Text: tokenText(0)}); err != nil {
		return err
	}
	if s.Count == 1 {
		return nil
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for i := 1; i < s.Count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := fn(Token{Index: i, Text: tokenText(i)}); err != nil {
				return err
			}
		}
	}
	return nil
}
