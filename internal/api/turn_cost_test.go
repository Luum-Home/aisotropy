package api

import (
	"context"
	"math"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// These tests pin the reasoning-token invariant that ADR-0061 declares and that
// the cost path violated: llm.Usage.ReasoningTokens is a BREAKDOWN of
// OutputTokens (a subset already inside it), never a line to add on top. Both
// cost sites — streaming and non-streaming — priced output + reasoning, so every
// reasoning turn on an OpenAI-compatible provider was over-charged by exactly
// the reasoning slice.

const (
	testInPricePer1M  = 3.0
	testOutPricePer1M = 15.0
)

// costFloatTolerance absorbs float64 arithmetic noise without being loose enough
// to hide the bug under test (the double charge here is 0.0075 USD, ~1000x this).
const costFloatTolerance = 1e-9

// TestTurnCostUSD_ReasoningIsInsideOutput is the direct unit on the single helper
// that owns the invariant: a usage whose OutputTokens already contains its
// ReasoningTokens must cost exactly what its OutputTokens cost.
func TestTurnCostUSD_ReasoningIsInsideOutput(t *testing.T) {
	reasoning := llm.Usage{InputTokens: 1000, OutputTokens: 2000, ReasoningTokens: 500}
	plain := llm.Usage{InputTokens: 1000, OutputTokens: 2000}

	withReasoning := turnCostUSD(reasoning, testInPricePer1M, testOutPricePer1M)
	withoutReasoning := turnCostUSD(plain, testInPricePer1M, testOutPricePer1M)

	if math.Abs(withReasoning-withoutReasoning) > costFloatTolerance {
		t.Errorf("cost with reasoning = %v, without = %v: identical OutputTokens must cost the same. "+
			"reasoning_tokens is a subset of completion_tokens, not an extra line", withReasoning, withoutReasoning)
	}

	want := costmeter.EstimateCostUSD(1000, 2000, testInPricePer1M, testOutPricePer1M)
	if math.Abs(withReasoning-want) > costFloatTolerance {
		t.Errorf("cost = %v, want %v (input + output at the given price basis)", withReasoning, want)
	}
}

// TestRecordStreamCost_DoesNotDoubleChargeReasoning covers the streaming cost
// site end to end through the meter.
func TestRecordStreamCost_DoesNotDoubleChargeReasoning(t *testing.T) {
	meter := costmeter.New()
	deps := StreamDeps{Meter: meter, InPricePer1M: testInPricePer1M, OutPricePer1M: testOutPricePer1M}
	recordStreamCost(deps, "acme", &llm.Usage{InputTokens: 1000, OutputTokens: 2000, ReasoningTokens: 500})

	s, ok := meter.Stats("acme")
	if !ok {
		t.Fatal("the turn was not recorded at all")
	}
	want := costmeter.EstimateCostUSD(1000, 2000, testInPricePer1M, testOutPricePer1M)
	if math.Abs(s.CostUSD-want) > costFloatTolerance {
		t.Errorf("CostUSD = %v, want %v (the 500 reasoning tokens are already inside the 2000 output tokens)", s.CostUSD, want)
	}
	// The reasoning count itself is still reported: it is a visibility breakdown,
	// and dropping it would hide how much of the spend is hidden chain-of-thought.
	if s.ReasoningTokens != 500 {
		t.Errorf("ReasoningTokens = %d, want 500: the breakdown must stay visible in /costs", s.ReasoningTokens)
	}
	if s.OutputTokens != 2000 {
		t.Errorf("OutputTokens = %d, want 2000: the provider's completion count is reported verbatim", s.OutputTokens)
	}
}

// TestChatRecordTurn_DoesNotDoubleChargeReasoning covers the non-streaming cost
// site. Both sites must agree, or /costs means different things per route.
func TestChatRecordTurn_DoesNotDoubleChargeReasoning(t *testing.T) {
	meter := costmeter.New()
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	usage := llm.Usage{InputTokens: 1000, OutputTokens: 2000, ReasoningTokens: 500}

	chatRecordTurn(context.Background(), wm, key, "acme", "reply", usage, meter)

	s, ok := meter.Stats("acme")
	if !ok {
		t.Fatal("the turn was not recorded at all")
	}
	want := costmeter.EstimateCostUSD(1000, 2000, costInputPricePer1M, costOutputPricePer1M)
	if math.Abs(s.CostUSD-want) > costFloatTolerance {
		t.Errorf("CostUSD = %v, want %v (reasoning is inside output, not additive)", s.CostUSD, want)
	}
}

// TestTurnCost_StreamingAndNonStreamingAgree proves the two routes price an
// identical reasoning turn identically — the property that broke silently when
// the invariant lived duplicated in two files.
func TestTurnCost_StreamingAndNonStreamingAgree(t *testing.T) {
	usage := llm.Usage{InputTokens: 700, OutputTokens: 1300, ReasoningTokens: 400}

	streamMeter := costmeter.New()
	recordStreamCost(
		StreamDeps{Meter: streamMeter, InPricePer1M: costInputPricePer1M, OutPricePer1M: costOutputPricePer1M},
		"acme", &usage)

	chatMeter := costmeter.New()
	chatRecordTurn(context.Background(), newTestWorkingMemory(t),
		memory.Key{Tenant: "acme", User: "alice", Session: "s1"}, "acme", "reply", usage, chatMeter)

	streamStats, _ := streamMeter.Stats("acme")
	chatStats, _ := chatMeter.Stats("acme")
	if math.Abs(streamStats.CostUSD-chatStats.CostUSD) > costFloatTolerance {
		t.Errorf("streaming cost %v != non-streaming cost %v for the same usage at the same price basis",
			streamStats.CostUSD, chatStats.CostUSD)
	}
}
