// Usage accumulation at the client boundary (Element E of the arena adoption
// audit; product half of the "honest per-turn spend" the arena adapter used to
// hand-roll).
//
// # Why this is a BASE-client decorator, not a ModelMiddleware
//
// The one choke point EVERY provider call in the runtime passes through is the
// base llm.Client resolved at aisotropy/app.go's startRuntime: the main turn
// call (ChatCore -> chatReflect -> GollemModel -> StreamChat), reflection
// retries, the native tool-calling path (RunToolAgent -> CallTools), the
// direct-streaming path (StreamCore -> StreamChat), AND the working-memory
// summarizer's own condensation calls (internal/memory/summarizer.go's
// LLMSummarizer.Condense -> StreamChat). That last one is decisive: the
// summarizer's client is the RAW base client (newWorkingMemory receives it at
// aisotropy/app.go BEFORE WithModelMiddleware composes the ADR-0062 chain), so
// a middleware-level observer would systematically UNDERCOUNT relative to the
// arena's hand-rolled decorator, which sat at the base and saw every call.
// WithUsageMeter therefore wraps the base client — the same slot the arena
// decorator occupied — and the runtime composes middleware/breaker/OTel ON TOP
// of the metered client, so the meter observes the all-calls honest total.
//
// # Scope of the counter
//
// A UsageMeter is App-scoped (one per aisotropy.App), NOT tenant-scoped: it is
// the process-level aggregate provider spend, exactly what the arena
// decorator's cumulative usage() provided. Per-tenant accounting remains the
// job of internal/costmeter (GET /costs) — the two answer different questions
// and neither replaces the other. The meter holds only integer token counts;
// no prompt text, no tenant data, no credentials.
package llm

import (
	"context"
	"sync"
)

// UsageMeter is a concurrency-safe accumulator of provider-reported token
// usage. Every observed Usage (a stream's terminal Token.Usage, a
// ToolCallResult.Usage) is folded into a running total. The zero value is
// ready to use; NewUsageMeter is provided for symmetry.
type UsageMeter struct {
	mu    sync.Mutex
	total Usage
	calls int64
}

// NewUsageMeter returns a fresh, zeroed meter.
func NewUsageMeter() *UsageMeter { return &UsageMeter{} }

// Add folds one provider-reported Usage into the running total and increments
// the observed-call count. Safe for concurrent use.
func (m *UsageMeter) Add(u Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total.InputTokens += u.InputTokens
	m.total.OutputTokens += u.OutputTokens
	m.total.ReasoningTokens += u.ReasoningTokens
	m.calls++
}

// Total returns the cumulative provider-reported token usage observed so far —
// across EVERY call path that traversed the metered client (main turns,
// reflection retries, tool-calling turns, summarizer condensations). It is a
// snapshot; concurrent calls may keep adding.
func (m *UsageMeter) Total() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

// Calls returns how many provider calls reported usage into this meter. A call
// whose provider returned no usage block (nil Usage) is not counted — the
// meter records real accounting data only, never a fabricated entry.
func (m *UsageMeter) Calls() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// WithUsageMeter wraps c so every provider-reported Usage — a stream's
// terminal Token.Usage AND a native tool-call's ToolCallResult.Usage — is
// folded into meter. Tokens and results are forwarded to the caller UNCHANGED;
// the wrapper only observes.
//
// Capability preservation (the interface-upgrade-loss trap this repo has been
// bitten by — see the 48dbf62 commit message): an inner ToolCaller yields a
// metered ToolCaller, and an inner ReasoningToolCaller yields a metered
// ReasoningToolCaller, so composing the meter never silently downgrades
// /chat/agent to the agentloop JSON fallback nor drops cross-step reasoning.
//
// A nil meter returns c unchanged (not even a zero-op wrapper), so an unmetered
// composition is byte-for-byte the pre-meter behavior.
func WithUsageMeter(c Client, meter *UsageMeter) Client {
	if meter == nil {
		return c
	}
	base := &usageClient{next: c, meter: meter}
	if rtc, ok := c.(ReasoningToolCaller); ok {
		return &usageReasoningToolCaller{
			usageToolCaller: &usageToolCaller{usageClient: base, tc: rtc},
			rtc:             rtc,
		}
	}
	if tc, ok := c.(ToolCaller); ok {
		return &usageToolCaller{usageClient: base, tc: tc}
	}
	return base
}

// usageClient meters the streaming path: it forwards every Token unchanged and
// folds a terminal Token's Usage (when the provider supplied one) into the
// meter. Observation happens on the RECEIVE side of the forwarding loop, so a
// terminal usage is counted even when the consumer stops reading at Done.
type usageClient struct {
	next  Client
	meter *UsageMeter
}

// Name reports the wrapped client's identity unchanged (Stub detection and
// observability rely on it).
func (u *usageClient) Name() string { return u.next.Name() }

// StreamChat opens the inner stream and forwards it through a metering loop.
// The forwarding honors ctx on both receive and send (the same discipline as
// otel.go's forwardTokens), so cancellation set by the caller still propagates
// and the goroutine never leaks on an abandoned consumer.
func (u *usageClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	in, err := u.next.StreamChat(ctx, system, prompt)
	if err != nil {
		return nil, err
	}
	out := make(chan Token)
	go u.forwardAndMeter(ctx, in, out)
	return out, nil
}

// forwardAndMeter ranges over in, folding any Token.Usage into the meter and
// forwarding every Token to out unchanged, until in closes, a terminal token
// (Done or Err) is forwarded, or ctx is cancelled.
func (u *usageClient) forwardAndMeter(ctx context.Context, in <-chan Token, out chan<- Token) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return
		case tok, ok := <-in:
			if !ok {
				return
			}
			if tok.Usage != nil {
				u.meter.Add(*tok.Usage)
			}
			select {
			case out <- tok:
			case <-ctx.Done():
				return
			}
			if tok.Done || tok.Err != nil {
				return
			}
		}
	}
}

// usageToolCaller preserves an inner ToolCaller through the meter and folds
// each ToolCallResult.Usage (carried by the non-streaming tool-call response
// since the CallTools usage fix) into the same meter — a native tool-call is
// provider spend too, and the arena decorator this replaces counted it.
type usageToolCaller struct {
	*usageClient
	tc ToolCaller
}

var _ ToolCaller = (*usageToolCaller)(nil)

// CallTools delegates to the inner ToolCaller and meters the result's Usage.
// The result is returned unchanged.
func (u *usageToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	res, err := u.tc.CallTools(ctx, system, prompt, tools)
	if res.Usage != nil {
		u.meter.Add(*res.Usage)
	}
	return res, err
}

// usageReasoningToolCaller preserves an inner ReasoningToolCaller through the
// meter: CallToolsReasoning is provider spend exactly like CallTools, and a
// decorator that dropped this capability would silently stop reasoning from
// being carried across agent steps (graceful degradation, but a degradation a
// metering wrapper has no business causing).
type usageReasoningToolCaller struct {
	*usageToolCaller
	rtc ReasoningToolCaller
}

var _ ReasoningToolCaller = (*usageReasoningToolCaller)(nil)

// CallToolsReasoning delegates to the inner ReasoningToolCaller and meters the
// result's Usage. Result and reasoning state are returned unchanged.
func (u *usageReasoningToolCaller) CallToolsReasoning(ctx context.Context, system, prompt string, tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	res, state, err := u.rtc.CallToolsReasoning(ctx, system, prompt, tools, prior)
	if res.Usage != nil {
		u.meter.Add(*res.Usage)
	}
	return res, state, err
}
