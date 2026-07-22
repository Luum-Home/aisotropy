package llm

// Capability-preservation guard for the ReasoningToolCaller tier (ADR-0061)
// across EVERY runtime decorator — the regression test for the
// interface-upgrade-loss trap found one layer deeper than the arena adapters
// (see the be945de commit message): breaker.go / otel.go / middleware.go
// preserved ToolCaller but silently dropped ReasoningToolCaller, so
// gollem.go's callToolsMaybeReasoning assertion (rtc, ok :=
// tc.(ReasoningToolCaller)) could NEVER succeed once the always-applied
// circuit breaker wrapped the client, and reasoning stopped being carried
// across agent steps for every consumer.
//
// The table covers each decorator individually AND the full chain composed in
// the exact aisotropy/app.go startRuntime order (base -> WithUsageMeter ->
// WithModelMiddleware -> WithCircuitBreaker -> WithOTel), in both directions:
// a reasoning-capable inner client must stay assertable to
// ReasoningToolCaller (and the call must reach the inner client), and a plain
// Client must NOT spuriously gain the capability.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"
)

// reasoningCapFake is a scriptable ReasoningToolCaller: it counts how many
// CallTools / CallToolsReasoning invocations actually reached it and can be
// forced to fail (for the breaker-semantics test). No network, no randomness.
type reasoningCapFake struct {
	Client // StubClient underneath, for StreamChat/Name

	toolCalls      int
	reasoningCalls int
	failWith       error // when non-nil, every tool call fails with this
}

func newReasoningCapFake() *reasoningCapFake {
	return &reasoningCapFake{Client: NewStubClient()}
}

func (f *reasoningCapFake) CallTools(_ context.Context, _, _ string, _ []ToolSpec) (ToolCallResult, error) {
	f.toolCalls++
	if f.failWith != nil {
		return ToolCallResult{}, f.failWith
	}
	return ToolCallResult{Message: "plain"}, nil
}

func (f *reasoningCapFake) CallToolsReasoning(_ context.Context, _, _ string, _ []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	f.reasoningCalls++
	if f.failWith != nil {
		return ToolCallResult{}, ReasoningState{}, f.failWith
	}
	// Echo the carry-forward contract: prior in, fresh reasoning out.
	_ = prior
	return ToolCallResult{Message: "reasoned"}, NewTextReasoningState("carried"), nil
}

var _ ReasoningToolCaller = (*reasoningCapFake)(nil)

// reasoningDecoratorTable lists every runtime decorator plus the full chain
// composed exactly as aisotropy/app.go's startRuntime does. Each wrap must
// select the widest capability tier its inner client supports.
func reasoningDecoratorTable() []struct {
	name string
	wrap func(Client) Client
} {
	tracer := noop.NewTracerProvider().Tracer("t")
	return []struct {
		name string
		wrap func(Client) Client
	}{
		{"usage_meter", func(c Client) Client {
			return WithUsageMeter(c, NewUsageMeter())
		}},
		{"model_middleware", func(c Client) Client {
			return WithModelMiddleware(c, &passthroughMiddleware{})
		}},
		{"circuit_breaker", func(c Client) Client {
			return WithCircuitBreaker(c, 5, 30*time.Second)
		}},
		{"otel", func(c Client) Client {
			return WithOTel(c, tracer)
		}},
		{"composed_chain_startRuntime_order", func(c Client) Client {
			// Verbatim startRuntime composition: base -> WithUsageMeter ->
			// WithModelMiddleware -> WithCircuitBreaker -> WithOTel.
			metered := WithUsageMeter(c, NewUsageMeter())
			middlewared := WithModelMiddleware(metered, &passthroughMiddleware{})
			protected := WithCircuitBreaker(middlewared, 5, 30*time.Second)
			return WithOTel(protected, tracer)
		}},
	}
}

// TestDecoratorsPreserveReasoningToolCaller: for every decorator (and the
// composed chain), wrapping a ReasoningToolCaller-capable inner client must
// yield a client still assertable to ReasoningToolCaller, with
// CallToolsReasoning reaching the inner client and the reasoning state
// forwarded unchanged.
func TestDecoratorsPreserveReasoningToolCaller(t *testing.T) {
	for _, d := range reasoningDecoratorTable() {
		t.Run(d.name, func(t *testing.T) {
			inner := newReasoningCapFake()
			wrapped := d.wrap(inner)

			rtc, ok := wrapped.(ReasoningToolCaller)
			if !ok {
				t.Fatalf("%s erased the ReasoningToolCaller capability", d.name)
			}

			res, state, err := rtc.CallToolsReasoning(context.Background(), "sys", "p", nil, NewTextReasoningState("prior"))
			if err != nil {
				t.Fatalf("%s CallToolsReasoning: %v", d.name, err)
			}
			if inner.reasoningCalls != 1 {
				t.Fatalf("%s CallToolsReasoning did not reach the inner client (reasoningCalls=%d)", d.name, inner.reasoningCalls)
			}
			if res.Message != "reasoned" {
				t.Fatalf("%s altered the result: %q", d.name, res.Message)
			}
			if got := state.ReasoningText(); got != "carried" {
				t.Fatalf("%s altered the returned ReasoningState: %q", d.name, got)
			}
		})
	}
}

// TestDecoratorsDoNotFabricateReasoningToolCaller: wrapping a plain Client
// must NOT make it a ReasoningToolCaller (nor a ToolCaller) — otherwise the
// agent loop would take the reasoning path against a client that cannot serve
// it.
func TestDecoratorsDoNotFabricateReasoningToolCaller(t *testing.T) {
	for _, d := range reasoningDecoratorTable() {
		t.Run(d.name, func(t *testing.T) {
			wrapped := d.wrap(NewStubClient()) // plain Client, no tool calling
			if _, ok := wrapped.(ReasoningToolCaller); ok {
				t.Fatalf("%s fabricated a ReasoningToolCaller from a plain Client", d.name)
			}
			if _, ok := wrapped.(ToolCaller); ok {
				t.Fatalf("%s fabricated a ToolCaller from a plain Client", d.name)
			}
		})
	}
}

// TestComposedChainReasoningReachesGollemCallSite is the direct proof for the
// exact call site the bug disabled: gollem.go's callToolsMaybeReasoning (the
// rtc, ok := tc.(ReasoningToolCaller) assertion) must succeed against the
// FULL startRuntime-composed chain and route through CallToolsReasoning,
// updating the request-local accumulator — not silently fall back to plain
// CallTools.
func TestComposedChainReasoningReachesGollemCallSite(t *testing.T) {
	inner := newReasoningCapFake()

	metered := WithUsageMeter(inner, NewUsageMeter())
	middlewared := WithModelMiddleware(metered, &passthroughMiddleware{})
	protected := WithCircuitBreaker(middlewared, 5, 30*time.Second)
	composed := WithOTel(protected, noop.NewTracerProvider().Tracer("t"))

	tc, ok := composed.(ToolCaller)
	if !ok {
		t.Fatal("composed chain is not even a ToolCaller")
	}

	acc := NewTextReasoningState("prior")
	m := NewGollemModel(composed).withReasoningAccumulator(&acc)

	res, err := m.callToolsMaybeReasoning(context.Background(), tc, "sys", "p", nil)
	if err != nil {
		t.Fatalf("callToolsMaybeReasoning: %v", err)
	}
	if inner.reasoningCalls != 1 || inner.toolCalls != 0 {
		t.Fatalf("expected the reasoning path (reasoningCalls=1, toolCalls=0), got reasoningCalls=%d toolCalls=%d — the composed chain fell back to plain CallTools",
			inner.reasoningCalls, inner.toolCalls)
	}
	if res.Message != "reasoned" {
		t.Fatalf("unexpected result: %q", res.Message)
	}
	if got := acc.ReasoningText(); got != "carried" {
		t.Fatalf("accumulator not updated with fresh reasoning: %q", got)
	}
}

// TestCircuitBreakerGuardsCallToolsReasoning: the breaker's protective
// semantics must not weaken — CallToolsReasoning failures count against the
// SAME breaker as CallTools failures (and vice versa), and an open breaker
// fails a reasoning call fast without reaching the provider.
func TestCircuitBreakerGuardsCallToolsReasoning(t *testing.T) {
	providerDown := errors.New("provider down")

	t.Run("reasoning_failures_trip_the_breaker", func(t *testing.T) {
		inner := newReasoningCapFake()
		inner.failWith = providerDown
		rtc, ok := WithCircuitBreaker(inner, 3, time.Hour).(ReasoningToolCaller)
		if !ok {
			t.Fatal("breaker erased ReasoningToolCaller")
		}

		for i := 0; i < 3; i++ {
			if _, _, err := rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{}); !errors.Is(err, providerDown) {
				t.Fatalf("call %d: expected provider error, got %v", i, err)
			}
		}
		// Threshold reached: the breaker is open. Both call paths must fail
		// fast WITHOUT reaching the inner client.
		if _, _, err := rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{}); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("expected ErrCircuitOpen on the reasoning path, got %v", err)
		}
		if _, err := rtc.CallTools(context.Background(), "s", "p", nil); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("expected ErrCircuitOpen on the plain tool path (shared breaker), got %v", err)
		}
		if inner.reasoningCalls != 3 || inner.toolCalls != 0 {
			t.Fatalf("open breaker leaked calls to the provider: reasoningCalls=%d toolCalls=%d", inner.reasoningCalls, inner.toolCalls)
		}
	})

	t.Run("plain_tool_failures_gate_the_reasoning_path", func(t *testing.T) {
		inner := newReasoningCapFake()
		inner.failWith = providerDown
		rtc, ok := WithCircuitBreaker(inner, 3, time.Hour).(ReasoningToolCaller)
		if !ok {
			t.Fatal("breaker erased ReasoningToolCaller")
		}

		for i := 0; i < 3; i++ {
			if _, err := rtc.CallTools(context.Background(), "s", "p", nil); !errors.Is(err, providerDown) {
				t.Fatalf("call %d: expected provider error, got %v", i, err)
			}
		}
		if _, _, err := rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{}); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("expected ErrCircuitOpen on the reasoning path after CallTools tripped the breaker, got %v", err)
		}
		if inner.toolCalls != 3 || inner.reasoningCalls != 0 {
			t.Fatalf("open breaker leaked calls to the provider: toolCalls=%d reasoningCalls=%d", inner.toolCalls, inner.reasoningCalls)
		}
	})

	t.Run("reasoning_success_closes_the_breaker", func(t *testing.T) {
		inner := newReasoningCapFake()
		inner.failWith = providerDown
		rtc, ok := WithCircuitBreaker(inner, 3, time.Hour).(ReasoningToolCaller)
		if !ok {
			t.Fatal("breaker erased ReasoningToolCaller")
		}

		// Two failures (below threshold), then recovery: a successful
		// reasoning call must reset the failure count like CallTools does.
		for i := 0; i < 2; i++ {
			_, _, _ = rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{})
		}
		inner.failWith = nil
		if _, _, err := rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{}); err != nil {
			t.Fatalf("recovered call failed: %v", err)
		}
		// Failure count reset: two MORE failures must not open the breaker
		// (threshold is 3 consecutive).
		inner.failWith = providerDown
		for i := 0; i < 2; i++ {
			if _, _, err := rtc.CallToolsReasoning(context.Background(), "s", "p", nil, ReasoningState{}); !errors.Is(err, providerDown) {
				t.Fatalf("post-recovery call %d: expected provider error (breaker closed), got %v", i, err)
			}
		}
	})
}
