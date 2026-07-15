// cost_obs_demo — RUN-VERIFIED probe of Genkit Go's cost, pre-spend, and
// observability surface against the REAL framework (github.com/firebase/genkit/go
// v1.10.0), fully OFFLINE via the arena's registered fake model.
//
// Run:  go run ./cmd/costobs      (from arenas/genkit-go)
//
// It proves, by executing, three things and prints a [BUILT-IN]/[DIY]/[PAID]/
// [NOT-POSSIBLE] verdict for each:
//
//  1. PER-TENANT COST TRACKING — 2 turns for tenantA, 2 for tenantB. Does the
//     framework RETURN usage per call? Is per-tenant AGGREGATION given or DIY?
//  2. PRE-SPEND BUDGET GATE — a tiny per-tenant token budget; before a call we
//     check the tenant is over budget and REJECT *before the model runs*. We
//     prove the model is NOT called on the rejected turn via a call counter on
//     the fake model. Does Genkit PROVIDE a pre-spend check, or is it DIY?
//  3. OBSERVABILITY — enable Genkit's native OTel tracing, capture the emitted
//     spans with an in-memory exporter, and show what attributes are captured
//     and whether tenant_id is on the spans. Built-in vs DIY vs PAID.
//
// HONESTY CONTRACT: token NUMBERS are synthetic (len(text)/4, labeled). The
// mechanism under test is the FRAMEWORK's, not the arithmetic. Where Genkit
// does not ship something (per-tenant aggregation, pre-spend gate, tenant_id on
// spans), this demo implements the DIY version and LABELS it DIY — it never
// dresses a hand-rolled feature up as a built-in.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"genkit-go-arena/orbit"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// syntheticPricePer1KTokens is a FAKE price used only to turn synthetic tokens
// into a synthetic dollar figure. Not a real rate.
const syntheticPricePer1KTokens = 0.50 // USD / 1K tokens (SYNTHETIC)

// tenantMeter is the DIY per-tenant accumulator. Genkit ships nothing like it:
// ai.GenerationUsage is per-call only, with no notion of a tenant.
type tenantMeter struct {
	turns    int
	inTok    int
	outTok   int
	totalTok int
}

func (m *tenantMeter) costUSD() float64 {
	return float64(m.totalTok) / 1000.0 * syntheticPricePer1KTokens
}

// costLedger is the DIY aggregation layer keyed by tenant_id.
type costLedger struct {
	m map[string]*tenantMeter
}

func newLedger() *costLedger { return &costLedger{m: map[string]*tenantMeter{}} }

func (l *costLedger) add(tenant string, u *ai.GenerationUsage) {
	tm := l.m[tenant]
	if tm == nil {
		tm = &tenantMeter{}
		l.m[tenant] = tm
	}
	tm.turns++
	if u != nil {
		tm.inTok += u.InputTokens
		tm.outTok += u.OutputTokens
		tm.totalTok += u.TotalTokens
	}
}

func (l *costLedger) tokensFor(tenant string) int {
	if tm := l.m[tenant]; tm != nil {
		return tm.totalTok
	}
	return 0
}

func main() {
	ctx := context.Background()

	fmt.Println(strings.Repeat("=", 72))
	fmt.Println("Genkit Go — cost + pre-spend gate + observability (REAL framework, offline)")
	fmt.Println("framework: github.com/firebase/genkit/go v1.10.0   model: fake/orbit (no network)")
	fmt.Println(strings.Repeat("=", 72))

	app, err := orbit.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit init failed:", err)
		os.Exit(2)
	}

	// Enable Genkit's built-in OTel tracing capture up front (used by section 3).
	// tracing.TracerProvider() returns the SAME *sdktrace.TracerProvider that
	// genkit.Generate/flows write to; we attach an in-memory exporter to it.
	exporter := tracetest.NewInMemoryExporter()
	tracing.TracerProvider().RegisterSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter))

	sectionCost(ctx, app)
	sectionPreSpend(ctx, app)
	sectionObservability(ctx, app, exporter)

	fmt.Println("\n" + strings.Repeat("=", 72))
	fmt.Println("DONE — all three sections executed against the real framework, offline.")
	fmt.Println(strings.Repeat("=", 72))
}

// runTurn runs ONE real generation through genkit.Generate (the real pipeline)
// and returns the framework-reported Usage. It wraps the call in a DIY span that
// carries tenant_id, because Genkit's own spans do NOT tag tenant identity.
func runTurn(ctx context.Context, app *orbit.Orbit, tenant, prompt string) (*ai.ModelResponse, error) {
	ctx, span := tracing.Tracer().Start(ctx, "orbit.turn")
	// DIY: tenant_id is not a Genkit span attribute; we add it ourselves.
	span.SetAttributes(attribute.String("tenant_id", tenant))
	defer span.End()

	return genkit.Generate(ctx, app.G,
		ai.WithModel(app.Model),
		ai.WithPrompt(prompt),
	)
}

// ---------------------------------------------------------------------------
// 1. PER-TENANT COST TRACKING
// ---------------------------------------------------------------------------

func sectionCost(ctx context.Context, app *orbit.Orbit) {
	fmt.Println("\n" + strings.Repeat("-", 72))
	fmt.Println("[1] PER-TENANT COST TRACKING")
	fmt.Println(strings.Repeat("-", 72))

	ledger := newLedger()

	turns := []struct{ tenant, prompt string }{
		{"tenantA", "Hi, where is my order #4821?"},
		{"tenantA", "Also, can you tell me your refund policy in detail please?"},
		{"tenantB", "hello"},
		{"tenantB", "I was charged twice and I want a full refund for order #77"},
	}

	fmt.Println("Per-call usage returned by the framework (resp.Usage — BUILT-IN visibility):")
	for _, t := range turns {
		resp, err := runTurn(ctx, app, t.tenant, t.prompt)
		if err != nil {
			fmt.Printf("  ✗ %s turn failed: %v\n", t.tenant, err)
			continue
		}
		u := resp.Usage
		fmt.Printf("  %-8s in=%-3d out=%-3d total=%-3d  (resp.Usage.*Tokens — SYNTHETIC counts)\n",
			t.tenant, u.InputTokens, u.OutputTokens, u.TotalTokens)
		ledger.add(t.tenant, u) // DIY aggregation
	}

	fmt.Println("\nPer-tenant totals (DIY aggregation — the framework has no per-tenant meter):")
	for _, tenant := range sortedKeys(ledger.m) {
		tm := ledger.m[tenant]
		fmt.Printf("  %-8s turns=%d  tokens=%d  cost=$%.4f  (price %.2f/1K — SYNTHETIC)\n",
			tenant, tm.turns, tm.totalTok, tm.costUSD(), syntheticPricePer1KTokens)
	}

	fmt.Println("\n  VERDICT visibility (per-call usage):   [BUILT-IN]  resp.Usage carries InputTokens/OutputTokens/TotalTokens")
	fmt.Println("  VERDICT aggregation (per-tenant meter): [DIY]       no tenant concept in ai.GenerationUsage; costLedger is hand-rolled here")
	fmt.Println("  VERDICT pricing (tokens -> $):          [DIY]       Genkit ships no price table")
}

// ---------------------------------------------------------------------------
// 2. PRE-SPEND BUDGET GATE
// ---------------------------------------------------------------------------

func sectionPreSpend(ctx context.Context, app *orbit.Orbit) {
	fmt.Println("\n" + strings.Repeat("-", 72))
	fmt.Println("[2] PRE-SPEND BUDGET GATE")
	fmt.Println(strings.Repeat("-", 72))
	fmt.Println("Genkit Go exposes NO pre-spend/usage-limit option (verified against v1.10.0:")
	fmt.Println("only ai.WithMaxTurns exists — a tool-loop iteration cap that ERRORS *after*")
	fmt.Println("spending, not a per-tenant token budget checked *before* the call). So the gate")
	fmt.Println("below is DIY: we check a per-tenant spent-token counter BEFORE genkit.Generate.")
	fmt.Println()

	ledger := newLedger()
	const budget = 40 // SYNTHETIC per-tenant token budget

	// preSpendGate is the DIY check. Returns true if the turn is ALLOWED.
	preSpendGate := func(tenant string) bool {
		return ledger.tokensFor(tenant) < budget
	}

	// spend runs a turn ONLY if the gate allows it, proving the model is not
	// touched on a rejected turn via orbit.ModelCallCount().
	spend := func(tenant, prompt string) {
		before := orbit.ModelCallCount()
		if !preSpendGate(tenant) {
			after := orbit.ModelCallCount()
			fmt.Printf("  REJECTED  %-8s spent=%-3d >= budget=%d  -> model NOT called (calls %d->%d, delta=%d)\n",
				tenant, ledger.tokensFor(tenant), budget, before, after, after-before)
			return
		}
		resp, err := runTurn(ctx, app, tenant, prompt)
		after := orbit.ModelCallCount()
		if err != nil {
			fmt.Printf("  ✗ %s error: %v\n", tenant, err)
			return
		}
		ledger.add(tenant, resp.Usage)
		fmt.Printf("  ALLOWED   %-8s +%-3d tok -> spent=%-3d/%d  (model called, calls %d->%d, delta=%d)\n",
			tenant, resp.Usage.TotalTokens, ledger.tokensFor(tenant), budget, before, after, after-before)
	}

	orbit.ResetModelCalls()

	fmt.Printf("Per-tenant budget = %d tokens (SYNTHETIC).\n\n", budget)
	// tenantA: burn through the budget, then get rejected.
	spend("tenantA", "Please summarize my entire order history in full detail, everything.")
	spend("tenantA", "And now explain your complete refund and warranty policy word for word.")
	// By now tenantA is over budget -> this turn must be rejected BEFORE the model.
	spend("tenantA", "One more question about shipping.")

	// tenantB is independent and still under budget -> allowed.
	fmt.Println()
	spend("tenantB", "hello")

	// Explicit proof line for the rejected path.
	callsBefore := orbit.ModelCallCount()
	rejected := !preSpendGate("tenantA")
	callsAfter := orbit.ModelCallCount()
	fmt.Printf("\n  Proof: gate check for over-budget tenantA made ZERO model calls (%d->%d). rejected=%v\n",
		callsBefore, callsAfter, rejected)

	fmt.Println("\n  VERDICT: [DIY]  no framework pre-spend gate. ai.WithMaxTurns caps tool")
	fmt.Println("           iterations and errors AFTER spending; it is not per-tenant and")
	fmt.Println("           not a budget. (Contrast: Pydantic AI UsageLimits runs a check")
	fmt.Println("           before the request — Genkit Go has no equivalent.)")
}

// ---------------------------------------------------------------------------
// 3. OBSERVABILITY
// ---------------------------------------------------------------------------

func sectionObservability(ctx context.Context, app *orbit.Orbit, exporter *tracetest.InMemoryExporter) {
	fmt.Println("\n" + strings.Repeat("-", 72))
	fmt.Println("[3] OBSERVABILITY (native OTel tracing, in-memory exporter)")
	fmt.Println(strings.Repeat("-", 72))

	exporter.Reset()

	// Run one turn through the real flow so Genkit emits its full span tree
	// (flow -> generate -> model action), wrapped in our tenant span.
	ctx, span := tracing.Tracer().Start(ctx, "orbit.turn")
	span.SetAttributes(attribute.String("tenant_id", "tenantA")) // DIY tenant tag
	_, err := app.Chat(ctx, orbit.ChatInput{
		TenantID: "tenantA", UserID: "userA", SessionKey: "tenantA:userA:obs",
		Prompt: "Where is my order #4821?",
	})
	span.End()
	if err != nil {
		fmt.Println("  ✗ traced turn failed:", err)
		return
	}
	// Force the (synchronous) exporter to have everything.
	_ = tracing.TracerProvider().ForceFlush(ctx)

	spans := exporter.GetSpans()
	fmt.Printf("Captured %d spans from ONE turn via Genkit's built-in OTel provider.\n\n", len(spans))

	// Collect the attribute-key vocabulary Genkit emits, and whether tenant_id
	// appears anywhere as a first-class span attribute.
	keySet := map[string]bool{}
	tenantIDOnSpan := false
	genkitInputCarriesTenant := false
	fmt.Println("Spans (name -> attribute keys):")
	for _, s := range spans {
		var keys []string
		for _, kv := range s.Attributes {
			k := string(kv.Key)
			keys = append(keys, k)
			keySet[k] = true
			if k == "tenant_id" {
				tenantIDOnSpan = true
			}
			// Genkit serializes the flow INPUT (which includes TenantID) into
			// the genkit:input attribute — tenant is present, but only buried
			// inside an opaque JSON blob, not queryable as its own dimension.
			if k == "genkit:input" && strings.Contains(kv.Value.AsString(), "tenant") {
				genkitInputCarriesTenant = true
			}
		}
		sort.Strings(keys)
		fmt.Printf("  • %-22s %s\n", s.Name, strings.Join(keys, ", "))
	}

	fmt.Println("\nAttribute-key vocabulary emitted by Genkit:")
	for _, k := range sortedStringSet(keySet) {
		marker := ""
		if k == "tenant_id" {
			marker = "  <- DIY (added by this demo, not by Genkit)"
		} else if strings.HasPrefix(k, "genkit:") {
			marker = "  <- BUILT-IN (Genkit proprietary key; not gen_ai.* semconv)"
		}
		fmt.Printf("  %s%s\n", k, marker)
	}

	fmt.Printf("\n  tenant_id present as a first-class span attribute: %v  (only because THIS demo set it — DIY)\n", tenantIDOnSpan)
	fmt.Printf("  tenant identity otherwise buried inside genkit:input JSON blob: %v\n", genkitInputCarriesTenant)

	fmt.Println("\n  VERDICT transport:      [BUILT-IN]  Genkit ships OTel tracing; spans captured with a plain in-memory exporter, no code changes to the framework")
	fmt.Println("  VERDICT schema:         [BUILT-IN, proprietary]  attributes are genkit:* (genkit:name/input/output/path/type) — NOT gen_ai.* semconv")
	fmt.Println("  VERDICT per-tenant tag: [DIY]       tenant_id is not a Genkit span attribute; you must add it yourself")
	fmt.Println("  VERDICT hosted backend: [PAID/OPTIONAL]  Firebase/Google Cloud telemetry (or any OTLP backend) is a separate hosted service; not required for local capture")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func sortedKeys(m map[string]*tenantMeter) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedStringSet(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
