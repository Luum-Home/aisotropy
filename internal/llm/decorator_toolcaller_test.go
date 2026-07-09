package llm

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"
)

// TestDecoratorsPreserveToolCaller is the regression guard for the wiring bug
// where WithCircuitBreaker / WithOTel erased a client's ToolCaller capability,
// silently disabling the native tool-calling path at the client.(ToolCaller)
// call site in the agent endpoint (ADR-0012 follow-up). Both decorators must
// keep a ToolCaller a ToolCaller and forward CallTools to the inner client.
// (fakeToolCaller is defined in toolagent_test.go; its turn counter proves the
// forwarded call reached the inner ToolCaller.)
func TestDecoratorsPreserveToolCaller(t *testing.T) {
	inner := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{{Message: "a"}, {Message: "b"}},
	}

	breakered := WithCircuitBreaker(inner, 5, 30*time.Second)
	tc, ok := breakered.(ToolCaller)
	if !ok {
		t.Fatal("WithCircuitBreaker erased the ToolCaller capability")
	}
	if _, err := tc.CallTools(context.Background(), "sys", "prompt", nil); err != nil {
		t.Fatalf("breakered CallTools: %v", err)
	}
	if inner.turn != 1 {
		t.Fatalf("breakered CallTools did not reach the inner ToolCaller (turn=%d)", inner.turn)
	}

	// Compose exactly as the runtime does: WithOTel over the breakered client.
	otelWrapped := WithOTel(breakered, noop.NewTracerProvider().Tracer("t"))
	tc2, ok := otelWrapped.(ToolCaller)
	if !ok {
		t.Fatal("WithOTel erased the ToolCaller capability")
	}
	if _, err := tc2.CallTools(context.Background(), "sys", "prompt", nil); err != nil {
		t.Fatalf("otel+breakered CallTools: %v", err)
	}
	if inner.turn != 2 {
		t.Fatalf("otel+breakered CallTools did not reach the inner ToolCaller (turn=%d)", inner.turn)
	}
}

// TestDecoratorsDoNotFabricateToolCaller: wrapping a plain Client (no native
// tool-calling) must NOT make it a ToolCaller — otherwise the agent endpoint
// would take the native path against a client that cannot serve it.
func TestDecoratorsDoNotFabricateToolCaller(t *testing.T) {
	plain := NewStubClient() // StubClient is a plain Client, not a ToolCaller
	if _, ok := WithCircuitBreaker(plain, 5, 30*time.Second).(ToolCaller); ok {
		t.Fatal("WithCircuitBreaker fabricated a ToolCaller from a plain Client")
	}
	if _, ok := WithOTel(plain, noop.NewTracerProvider().Tracer("t")).(ToolCaller); ok {
		t.Fatal("WithOTel fabricated a ToolCaller from a plain Client")
	}
}
