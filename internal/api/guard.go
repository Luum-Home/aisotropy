package api

// guard.go wires the two request-path capability seams shared by every chat
// handler:
//
//   - Guardrails (internal/guardrails): input redaction + injection detection
//     ALWAYS run; blocking is opt-in. The operator-fixed posture is
//     annotate + log by default, so secrets are scrubbed and injections are
//     flagged on every request, but a request/response is only rejected when
//     the corresponding block toggle is explicitly enabled.
//   - Gateway (internal/gateway): the model-selection seam. selectClient runs
//     the routing policy to pick the client for a turn; the streaming path's
//     quota-pressure degrade (llm.DegradeAt) still applies as an OVERRIDE on
//     top of the pick, so the gateway complements — never replaces — it.
//
// Both seams are nil-safe: a nil *Guard or nil *gateway.Gateway makes the
// corresponding helper a pass-through, so a handler wired without them behaves
// exactly as it did before this integration (and the existing tests that pass
// nil keep working unchanged).

import (
	"context"
	"log/slog"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// outputGuardRefusal is the safe text substituted for a model response that
// tripped the output guardrail when hard output-blocking is enabled. It carries
// none of the flagged (toxic/leaky) content.
const outputGuardRefusal = "I'm sorry, but I can't help with that request."

// Guard bundles the process-wide *guardrails.Guardrails with the two opt-in
// blocking toggles, threaded into every chat handler. A nil *Guard (or a Guard
// whose G is nil) disables the guardrails entirely — used by tests that pass
// nil. When G is non-nil, redaction and injection/output detection ALWAYS run;
// the Block* flags gate only whether a suspicious verdict actually rejects the
// request (BlockInjection) or replaces the response (BlockOutput). Default
// posture: annotate + log, never block.
type Guard struct {
	// G is the shared guardrails aggregator. nil disables all guardrails.
	G *guardrails.Guardrails
	// BlockInjection, when true, rejects an input whose injection verdict is
	// Suspicious with a 4xx instead of merely annotating it. Default: false.
	BlockInjection bool
	// BlockOutput, when true, replaces a flagged (toxic/leaky) response with a
	// safe refusal instead of merely annotating it. Default: false. It governs
	// BOTH chat routes: the non-streaming route substitutes the refusal after
	// the model returns (checkOutput), and the streaming route BUFFERS the whole
	// response (never flushing tokens live) so it, too, can inspect the assembled
	// text and refuse before emitting a single byte (see RegisterStream). When
	// false, the streaming route keeps its live token-by-token flush and
	// annotate-only posture (annotateOutput) — no latency change.
	BlockOutput bool
}

// outputBlockingEnabled reports whether hard output-blocking is active: a
// non-nil Guard, a non-nil aggregator (nil disables all guardrails), and the
// BlockOutput toggle on. Nil-safe so a guardless handler never blocks. The
// streaming route consults this to decide between the buffered blocking path
// and the live annotate-only path.
func (g *Guard) outputBlockingEnabled() bool {
	return g != nil && g.G != nil && g.BlockOutput
}

// checkInput runs the input guardrails and returns the text to forward
// downstream (to memory hydration/persistence and the model) plus whether the
// request must be blocked. Redaction is fail-open: the returned text is always
// safe to forward. When g (or g.G) is nil it echoes prompt unchanged and never
// blocks, so a guardless handler is unchanged.
//
// The returned text is the REDACTED text whenever a Guard is configured, so a
// secret in the prompt never reaches the model NOR persists in any memory tier.
// Logging is types + counts (Redaction) and signal names (Injection) only —
// never the redacted values.
func (g *Guard) checkInput(ctx context.Context, tenant, user, prompt string) (forwarded string, blocked bool) {
	if g == nil || g.G == nil {
		return prompt, false
	}
	res := g.G.CheckInput(prompt)
	if res.Redaction.Total > 0 {
		slog.InfoContext(ctx, "input guardrail redacted sensitive data",
			"tenant_id", tenant, "user_id", user,
			"redaction_total", res.Redaction.Total,
			"redactions", res.Redaction.Counts,
		)
	}
	if res.Injection.Suspicious {
		slog.WarnContext(ctx, "input guardrail flagged possible prompt injection",
			"tenant_id", tenant, "user_id", user,
			"signals", res.Injection.Signals,
			"blocking", g.BlockInjection,
		)
	}
	return res.Redacted, g.BlockInjection && res.Injection.Suspicious
}

// checkOutput runs the output guardrails on a NON-STREAMING model response and
// returns the text to actually send to the client: the model output unchanged
// when no Guard is configured or when a flag-but-don't-block posture is in
// force (annotate + log), or the safe refusal when BlockOutput is enabled AND
// the response tripped a guardrail (toxicity or leak). The flagged text is
// never returned when blocking. Logs verdicts only, never the response body.
func (g *Guard) checkOutput(ctx context.Context, tenant, user, input, output, system string) string {
	if g == nil || g.G == nil {
		return output
	}
	res := g.G.CheckOutput(ctx, input, output, system)
	if res.Blocked {
		slog.WarnContext(ctx, "output guardrail flagged model response",
			"tenant_id", tenant, "user_id", user,
			"toxicity", res.Toxicity.Suspicious,
			"leak", res.Leak.Suspicious,
			"blocking", g.BlockOutput,
		)
		if g.BlockOutput {
			return outputGuardRefusal
		}
	}
	return output
}

// annotateOutput runs the output guardrails on the ASSEMBLED final text of a
// completed stream and logs (never blocks) when it trips a guardrail. Hard
// output-blocking on the streaming route is a documented follow-on: a mid-stream
// response has already been sent token by token and cannot be un-sent, so
// enforcing a refusal would require buffering the whole response before the
// first byte (defeating streaming). This is annotate-only by construction — it
// does NOT consult BlockOutput.
func (g *Guard) annotateOutput(ctx context.Context, tenant, user, input, output, system string) {
	if g == nil || g.G == nil {
		return
	}
	res := g.G.CheckOutput(ctx, input, output, system)
	if res.Blocked {
		slog.WarnContext(ctx, "output guardrail flagged streamed response (annotate-only; hard blocking on the streaming route is a follow-on that needs response buffering)",
			"tenant_id", tenant, "user_id", user,
			"toxicity", res.Toxicity.Suspicious,
			"leak", res.Leak.Suspicious,
		)
	}
}

// selectClient runs the model-selection gateway seam and returns the client to
// use for this turn. When gw is nil it returns fallback unchanged, preserving
// the pre-gateway behavior (and keeping guardless/gatewayless tests working).
//
// It projects the request down to a gateway.Request carrying the tenant's
// cumulative spend (from the shared cost meter) so a cost-budget policy can
// shape an over-budget tenant to the cheap target. PromptTokensEstimate is
// populated as honest routing metadata, but NO policy routes on prompt size —
// Bet-4 refuted length/compressibility as a routing predictor, so there is
// deliberately no length rule.
//
// modelPreference is the caller's explicit, optional model override
// (ChatRequest.ModelPreference): it maps onto gateway.Request.UISelector so
// gateway.OverridePolicy honors it as a hard target choice that wins over the
// cost-budget rule. Empty (the default) leaves UISelector empty, OverridePolicy
// abstains, and routing is unchanged. The streaming path's quota-pressure
// DegradeAt still overrides even an explicit preference (the safety valve),
// because it is applied AFTER this pick.
//
// A hard routing miss (policy target unresolved AND no default configured)
// falls back to `fallback`: a routing decision must never drop a request on the
// floor. An unknown/unregistered preference is likewise resolved to the default
// at Route time, so a client-supplied value can never drop the request. The
// Gateway itself already logs the miss.
func selectClient(ctx context.Context, gw *gateway.Gateway, fallback llm.Client, meter *costmeter.Meter, tenant, user, prompt, modelPreference string) llm.Client {
	if gw == nil {
		return fallback
	}
	var spent float64
	if meter != nil {
		if s, ok := meter.Stats(tenant); ok {
			spent = s.CostUSD
		}
	}
	req := gateway.Request{
		Prompt:               prompt,
		PromptTokensEstimate: len(prompt) / 4,
		Tenant:               tenant,
		User:                 user,
		CostSpentUSD:         spent,
		UISelector:           modelPreference,
	}
	client, _, err := gw.Route(ctx, req)
	if err != nil || client == nil {
		return fallback
	}
	return client
}
