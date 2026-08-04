package api

import (
	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// turnCostUSD is the SINGLE place that converts one turn's provider-reported
// token usage into an estimated USD cost. Both cost sites — the streaming
// pipeline (recordStreamCost) and the non-streaming route (chatRecordTurn) —
// call it, so the two routes cannot price the same turn differently.
//
// THE INVARIANT (ADR-0061): usage.ReasoningTokens is a BREAKDOWN OF
// usage.OutputTokens, never an addition to it. Every provider that surfaces a
// reasoning count reports it as a subset of the completion tokens it already
// billed:
//
//   - OpenAI-compatible providers report it as
//     completion_tokens_details.reasoning_tokens — a DETAIL of completion_tokens,
//     which internal/llm maps verbatim onto OutputTokens
//     (internal/llm/openai_compat.go). That the detail is a subset is also what
//     makes the OpenAI-contract total (prompt + completion) balance; our own
//     compat shim computes exactly that total (internal/api/openai_compat.go).
//   - Anthropic bills thinking INSIDE output_tokens and surfaces no separate
//     streaming count, so ReasoningTokens stays 0 there (internal/llm/llm.go).
//
// So the cost of a turn is input + output, full stop. Pricing reasoning as an
// extra line charged it twice on every OpenAI-compatible reasoning turn — the
// exact defect ADR-0061 declared handled. The reasoning count is still recorded
// on the meter (RecordUsage) as a visibility breakdown; it is just never priced
// a second time.
//
// If a provider ever reports reasoning tokens ADDITIVE to its completion count,
// this function — and only this function — is where that provider's usage would
// need normalising before pricing. Keep the arithmetic here.
func turnCostUSD(usage llm.Usage, inPricePer1M, outPricePer1M float64) float64 {
	return costmeter.EstimateCostUSD(usage.InputTokens, usage.OutputTokens, inPricePer1M, outPricePer1M)
}
