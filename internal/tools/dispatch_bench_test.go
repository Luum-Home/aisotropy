package tools

// dispatch_bench_test.go measures the cost of the behavior axis of granular
// control (docs/architecture/granular-control.md): turning a request's granted
// scopes into (a) the subset of tools that survive the scope gate and (b) the
// system-prompt fragment that is the ONLY thing the model is ever told about.
// This is a per-request hot path — it runs on every turn, on the request
// goroutine, before the LLM call — so its overhead is pure framework tax the
// caller pays whether or not any tool is ever invoked. The model is held out of
// this benchmark entirely (there is no LLM call here), so every nanosecond
// reported is the registry's own work: the scope-set build plus the ordered
// filter (Permitted) and the string assembly (PromptFragment).
//
// These complement, not duplicate, the correctness tests in tools_test.go:
// those prove a forbidden tool never leaks; these prove the gate that enforces
// that is cheap enough to run unconditionally on every request.

import (
	"encoding/json"
	"fmt"
	"testing"
)

// benchRegistry builds a registry of n tools where roughly half are gated
// behind a distinct RequiredScope and the rest are always-available. It mirrors
// the shape a real deployment registers: a handful of open tools plus a longer
// tail of permissioned ones. Every gated tool also carries a small JSON Schema
// (Parameters), matching a typed-tool registration (ADR-0016 lever #1), so the
// Tool values are realistically sized rather than empty structs.
func benchRegistry(n int) *Registry {
	params := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}}}`)
	ts := make([]Tool, 0, n)
	for i := range n {
		t := Tool{
			Name:        fmt.Sprintf("tool_%03d", i),
			Description: "a representative agent capability with a one-line description",
		}
		if i%2 == 1 {
			t.RequiredScope = fmt.Sprintf("scope:%03d", i)
			t.Parameters = params
		}
		ts = append(ts, t)
	}
	return NewRegistry(ts...)
}

// benchScopes returns a granted-scope set that unlocks about half of the gated
// tools in a registry of size n — a realistic "this caller can do some, not
// all" grant, which is the interesting case for the gate (neither everything
// nor nothing is permitted).
func benchScopes(n int) []string {
	var out []string
	for i := 1; i < n; i += 2 {
		if i%4 == 1 { // grant half of the gated (odd-index) tools
			out = append(out, fmt.Sprintf("scope:%03d", i))
		}
	}
	return out
}

// BenchmarkRegistryPermitted measures the scope-gated selection alone (no
// string rendering): build the granted-scope lookup set, then filter the
// ordered tool list. Reported per registry size so the O(tools + scopes) cost
// model is visible rather than asserted.
func BenchmarkRegistryPermitted(b *testing.B) {
	for _, n := range []int{4, 16, 64} {
		reg := benchRegistry(n)
		scopes := benchScopes(n)
		b.Run(fmt.Sprintf("tools=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for range b.N {
				sink += len(reg.Permitted(scopes))
			}
			_ = sink
		})
	}
}

// BenchmarkRegistryPromptFragment measures the full injection path a request
// actually pays: Permitted (the scope gate) plus the strings.Builder assembly
// of the system-prompt fragment. This is the real per-turn cost, since the
// fragment — not the raw registry — is what hydration folds into the prompt.
func BenchmarkRegistryPromptFragment(b *testing.B) {
	for _, n := range []int{4, 16, 64} {
		reg := benchRegistry(n)
		scopes := benchScopes(n)
		b.Run(fmt.Sprintf("tools=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for range b.N {
				sink += len(reg.PromptFragment(scopes))
			}
			_ = sink
		})
	}
}

// BenchmarkRegistryPromptFragmentNoScopes measures the unprivileged-caller path:
// no granted scopes at all. Over benchRegistry's mixed set every gated tool is
// correctly dropped, but the always-available (unscoped) half still renders — so
// this is the "only the open tools survive the gate" cost, reported separately
// from the mixed-grant numbers above rather than a pathological empty-fragment
// case. A registry whose every tool is gated would return "" here for a fraction
// of this cost (an early return before any string assembly).
func BenchmarkRegistryPromptFragmentNoScopes(b *testing.B) {
	reg := benchRegistry(64)
	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for range b.N {
		sink += len(reg.PromptFragment(nil))
	}
	_ = sink
}
