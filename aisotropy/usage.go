// Package aisotropy — the façade usage-observability surface (Element E of the
// arena adoption audit).
//
// Before this file existed, a consumer who wanted to know their provider token
// spend had to hand-roll a usage-counting decorator around the client they
// passed to WithLLM — which is exactly how the arena adapter fell into the
// interface-upgrade-loss trap (a StreamChat-only decorator silently dropped
// ToolCaller and downgraded /chat/agent to the JSON fallback). The runtime now
// meters usage itself, at the base-client boundary, so no consumer ever needs a
// custom client just to observe spend.
package aisotropy

import "github.com/Luum-Home/aisotropy/internal/llm"

// usageAccumulator lazily builds (once) and returns the App-scoped usage
// meter. startRuntime composes it into the base LLM client(s); App.Usage()
// reads it. Calling this before any runtime boot is cheap and side-effect-free
// beyond allocating the zeroed meter.
func (a *App) usageAccumulator() *llm.UsageMeter {
	a.usageOnce.Do(func() { a.usageAcc = llm.NewUsageMeter() })
	return a.usageAcc
}

// Usage returns the App's CUMULATIVE provider-reported token usage across its
// lifetime — the sum over EVERY provider call the runtime has made through its
// composed client(s): main chat turns, reflection retries, native tool-calling
// turns (ToolCallResult.Usage), streaming turns, quota-pressure cheap-model
// calls, AND the working-memory summarizer's own condensation calls. That
// all-calls scope is deliberate: it is the honest per-process spend (the same
// total the arena's hand-rolled usage decorator accumulated), and a figure
// that omitted the side-calls would systematically undercount what the
// provider actually bills.
//
// Two usage surfaces, two questions — do not mistake one for the other:
//
//   - App.Usage() (this method): CUMULATIVE, all calls, App-scoped. "What has
//     this process spent so far?"
//   - ChatOutput.Usage (App.Turn) / the HTTP /chat response's "usage" field:
//     PER-TURN, the turn's own main model call only. "What did this turn's
//     reply cost?" A turn's figure excludes summarizer condensations and any
//     other background call that happened to run alongside it.
//
// Per-TENANT accounting is a third, separate surface: the /costs meter
// (internal/costmeter), which prices usage in USD per tenant. App.Usage()
// holds token counts only, never per-tenant attribution.
//
// It never triggers a model call or a runtime boot: before the first Turn/Run
// the meter exists but has observed nothing, so the zero Usage is returned.
// Safe for concurrent use.
func (a *App) Usage() Usage {
	return a.usageAccumulator().Total()
}
