package main

import (
	"testing"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// TestAgnesClient_PreservesToolCaller is the belt-and-suspenders regression
// guard for the exact bug commit 48dbf62 diagnosed: the arena's usage-capturing
// client, injected via WithLLM, MUST still satisfy aisotropy.ToolCaller after
// the wrap — otherwise /chat/agent silently downgrades to the agentloop JSON
// fallback (the tools dimension's run-to-run variance source). The decorator
// forwards CallTools, so the assertion must hold. If a future refactor drops
// CallTools, the compile-time assertion in agnes_client.go breaks first; this
// test catches the case where the type still compiles but a decorator is
// slipped in that forwards only StreamChat.
func TestAgnesClient_PreservesToolCaller(t *testing.T) {
	// Construct a decorator directly (no network, no env): the wrap itself is
	// what's under test, not the inner client's transport.
	inner := aisotropy.NewOpenAICompatLLM("test-key", "agnes-2.0-flash", "https://example.invalid/v1", "agnes")
	caller, ok := inner.(aisotropy.ToolCaller)
	if !ok {
		t.Fatal("precondition failed: NewOpenAICompatLLM must implement aisotropy.ToolCaller (product half, commit 48dbf62)")
	}
	dec := &agnesClient{inner: inner, caller: caller}

	// The load-bearing assertion: the decorator, viewed through the narrow
	// LLMClient interface the way WithLLM stores it, must up-assert to ToolCaller.
	var c aisotropy.LLMClient = dec
	if _, ok := c.(aisotropy.ToolCaller); !ok {
		t.Fatal("agnesClient decorator does not satisfy aisotropy.ToolCaller — /chat/agent would use the agentloop JSON fallback (the exact downgrade this rewrite prevents)")
	}
}

// TestAgnesClient_NameComposition proves Name() delegates to the inner façade
// client and composes the "agnes:<model>" string the arena reports.
func TestAgnesClient_NameComposition(t *testing.T) {
	inner := aisotropy.NewOpenAICompatLLM("k", "agnes-2.0-flash", "https://example.invalid/v1", "agnes")
	caller, _ := inner.(aisotropy.ToolCaller)
	dec := &agnesClient{inner: inner, caller: caller}
	if got, want := dec.Name(), "agnes:agnes-2.0-flash"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}
