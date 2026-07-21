package main

import (
	"testing"
	"time"
)

// TestInvocationTimeout_SingleTurnUnchanged locks in the pre-fix behavior for
// every mode except memory's persist invocation: qa/rag/tools/structured/
// stream, and memory mode's own recall invocation, all drive exactly one real
// model turn per adapter process, so they must keep the original flat
// baseInvocationTimeout (180s) exactly — no regression in the common case.
func TestInvocationTimeout_SingleTurnUnchanged(t *testing.T) {
	cases := []struct {
		name string
		in   adapterInput
	}{
		{"no messages (tools/qa shape with system-only content)", adapterInput{Mode: "qa"}},
		{"single user turn (qa/rag/tools/structured/stream)", adapterInput{Mode: "qa", Messages: []inMessage{{Role: "user", Content: "hi"}}}},
		{"single user turn with a system prompt", adapterInput{Mode: "structured", System: "be terse", Messages: []inMessage{{Role: "user", Content: "hi"}}}},
		{"memory recall invocation (buildMemory's second invocation)", adapterInput{Mode: "memory", Messages: []inMessage{{Role: "user", Content: "what did I say?"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := invocationTimeout(tc.in)
			if got != baseInvocationTimeout {
				t.Fatalf("invocationTimeout(%+v) = %v, want unchanged baseInvocationTimeout %v", tc.in, got, baseInvocationTimeout)
			}
		})
	}
}

// TestInvocationTimeout_MultiTurnScales is the regression test for the
// mem-h03-longhaul incident: arenas/live/aisotropy's mode_memory.go runs ALL
// of a memory task's persist-side user turns sequentially inside ONE adapter
// process (one App.Turn per user message), but runWorkItem previously handed
// every invocation the SAME flat 180s outer timeout regardless of how many
// turns it actually contained. A long-haul task like mem-h03-longhaul (10
// persist user turns) reliably blew that budget once even one turn hit the
// product's constitutional fail-closed reflection retry loop
// (internal/llm/reflect_auto.go), which can cost 60-110s on its own — the
// whole adapter process was then killed mid-turn by the runner's outer
// context, surfacing as an opaque "killed" error instead of the adapter's
// own graceful "App.Turn failed on turn N".
//
// This test proves invocationTimeout scales with the invocation's own user
// turn count (buildMemory in tasks.go packs every persist turn into one
// adapterInput.Messages slice), entirely offline — no adapter process, no
// network call, no real model.
func TestInvocationTimeout_MultiTurnScales(t *testing.T) {
	longhaul := adapterInput{
		Mode: "memory",
		Messages: []inMessage{
			{Role: "user", Content: "turn 1"},
			{Role: "assistant", Content: "ack 1"},
			{Role: "user", Content: "turn 2"},
			{Role: "assistant", Content: "ack 2"},
			{Role: "user", Content: "turn 3"},
		},
	}
	got := invocationTimeout(longhaul)
	want := 3 * perTurnAdapterBudget // 3 user turns
	if got != want {
		t.Fatalf("invocationTimeout(3 user turns) = %v, want %v (perTurnAdapterBudget scaled)", got, want)
	}
	if got <= baseInvocationTimeout {
		t.Fatalf("invocationTimeout(3 user turns) = %v must exceed the flat baseInvocationTimeout %v — this is exactly the regression that killed mem-h03-longhaul mid-flight", got, baseInvocationTimeout)
	}

	// mem-h03-longhaul's actual shape: 10 persist user turns.
	tenTurns := adapterInput{Mode: "memory"}
	for i := 0; i < 10; i++ {
		tenTurns.Messages = append(tenTurns.Messages, inMessage{Role: "user", Content: "u"})
		tenTurns.Messages = append(tenTurns.Messages, inMessage{Role: "assistant", Content: "a"})
	}
	got10 := invocationTimeout(tenTurns)
	want10 := 10 * perTurnAdapterBudget
	if got10 != want10 {
		t.Fatalf("invocationTimeout(10 user turns, mem-h03-longhaul's shape) = %v, want %v", got10, want10)
	}
	// The per-turn adapter-side budget (mode_memory.go's own 110s
	// ctx.WithTimeout) must always fit strictly inside an even share of the
	// outer budget, so the adapter's own per-turn timeout always has a
	// chance to fire first and produce an informative error.
	const adapterPerTurnCtx = 110 * time.Second
	if perTurnAdapterBudget <= adapterPerTurnCtx {
		t.Fatalf("perTurnAdapterBudget (%v) must exceed the adapter's own per-turn ctx.WithTimeout (%v — mode_memory.go's runMemory) or the outer runner timeout can still race and kill a turn the adapter itself would have failed gracefully", perTurnAdapterBudget, adapterPerTurnCtx)
	}
}
