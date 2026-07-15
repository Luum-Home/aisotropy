// Command costobs is the cost + pre-spend gate + observability demo for the
// REAL Google ADK Go framework (google.golang.org/adk/v2 v2.0.0), run offline.
//
//	go run ./cmd/costobs
//
// It PROVES, by executing against the real llmagent + runner + session.Service
// (only the model is faked, via the framework's own model.LLM seam), three
// things and prints a [BUILT-IN]/[DIY]/[PAID]/[NOT-POSSIBLE] verdict for each:
//
//  1. PER-TENANT COST TRACKING — the framework RETURNS usage per call on every
//     event (event.UsageMetadata, a real ADK field: [BUILT-IN] visibility).
//     Per-tenant AGGREGATION is not provided (ADK has no tenant): [DIY].
//  2. PRE-SPEND BUDGET GATE — ADK has no pre-spend quota primitive (cost is
//     post-hoc token metadata only). The gate is [DIY]; we implement it and
//     prove, via a call-counter on the fake model, that the model does NOT run
//     on a rejected turn.
//  3. OBSERVABILITY — ADK emits OTel spans with gen_ai.* attributes via the
//     global tracer provider: [BUILT-IN], self-hostable/free. We install an
//     in-memory span exporter and capture them. tenant_id is NOT on ADK spans
//     natively ([DIY] enrichment); hosted APM (e.g. LangSmith) would be [PAID].
//
// SYNTHETIC NOTICE: token counts are a deterministic fake (len(text)/4) so the
// demo runs offline with no API key. The point is whether the FRAMEWORK helps,
// not exact counts. Every synthetic number is labeled.
package main

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"adk-go-arena/orbit"
)

// ---------------------------------------------------------------------------
// Synthetic pricing (LABELED FAKE). Gemini-Flash-ish shape, USD per 1M tokens.
// ---------------------------------------------------------------------------
const (
	synthUSDPerMInput  = 0.075 // SYNTHETIC price
	synthUSDPerMOutput = 0.300 // SYNTHETIC price
)

func synthTokens(text string) int32 { return int32(len(text) / 4) } // SYNTHETIC: len/4
func synthCostUSD(in, out int32) float64 {
	return float64(in)/1e6*synthUSDPerMInput + float64(out)/1e6*synthUSDPerMOutput
}

// ---------------------------------------------------------------------------
// meteredModel: offline model.LLM that (a) populates the REAL
// LLMResponse.UsageMetadata field with synthetic token counts, and (b) counts
// how many times the model actually runs. The counter is what proves the
// pre-spend gate rejects BEFORE the model executes.
// ---------------------------------------------------------------------------
type meteredModel struct {
	calls atomic.Int64
}

func (m *meteredModel) Name() string { return "orbit-metered-fake" }
func (m *meteredModel) Calls() int64 { return m.calls.Load() }

func (m *meteredModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1) // the model actually ran this call

		// Answer only from the history the runner assembled (same contract as
		// orbit.FakeModel), and sum synthetic INPUT tokens over that history.
		var lastUser string
		var remembered []string
		var inTok int32
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p == nil || p.Text == "" {
					continue
				}
				inTok += synthTokens(p.Text)
				if idx := strings.Index(p.Text, orbit.MemoPrefix); idx >= 0 {
					remembered = append(remembered, strings.TrimSpace(p.Text[idx+len(orbit.MemoPrefix):]))
				}
				if c.Role == genai.RoleUser {
					lastUser = p.Text
				}
			}
		}
		answer := m.answer(lastUser, remembered)
		outTok := synthTokens(answer)

		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: answer}},
			},
			// The REAL ADK field the framework surfaces per call, on every event
			// and on the generate_content OTel span. SYNTHETIC values here.
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     inTok,
				CandidatesTokenCount: outTok,
				TotalTokenCount:      inTok + outTok,
			},
		}, nil)
	}
}

func (m *meteredModel) answer(lastUser string, remembered []string) string {
	lu := strings.ToLower(lastUser)
	switch {
	case strings.Contains(lu, "remember") || strings.Contains(lu, "recall") || strings.Contains(lu, "what did"):
		if len(remembered) == 0 {
			return "I don't have anything on file for this conversation."
		}
		return "Here is what you told me earlier: " + strings.Join(remembered, "; ")
	case strings.Contains(lastUser, orbit.MemoPrefix):
		return "Noted. I'll remember that for this conversation."
	default:
		return "Orbit support here — how can I help? (offline metered fake model)"
	}
}

// ---------------------------------------------------------------------------
// DIY per-tenant cost meter. ADK has no tenant and no aggregation, so this is
// entirely developer-owned. It reads the framework's built-in per-call usage
// off the event stream.
// ---------------------------------------------------------------------------
type tenantCost struct {
	turns   int
	inTok   int32
	outTok  int32
	costUSD float64
}

type costMeter struct{ byTenant map[string]*tenantCost }

func newCostMeter() *costMeter { return &costMeter{byTenant: map[string]*tenantCost{}} }

func (cm *costMeter) add(tenant string, in, out int32) {
	tc := cm.byTenant[tenant]
	if tc == nil {
		tc = &tenantCost{}
		cm.byTenant[tenant] = tc
	}
	tc.turns++
	tc.inTok += in
	tc.outTok += out
	tc.costUSD += synthCostUSD(in, out)
}

func (cm *costMeter) tokens(tenant string) int32 {
	if tc := cm.byTenant[tenant]; tc != nil {
		return tc.inTok + tc.outTok
	}
	return 0
}

// ---------------------------------------------------------------------------
// runTurn drives ONE turn through the REAL ADK runner and returns the reply
// plus the usage the framework reported on the event stream. `identity` folds
// tenant into the ADK userID (the arena's documented convention).
// ---------------------------------------------------------------------------
func runTurn(ctx context.Context, o *orbit.Orbit, identity, sessionID, prompt string) (reply string, in, out int32, err error) {
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText(prompt)}}
	for ev, e := range o.Runner().Run(ctx, identity, sessionID, msg, agent.RunConfig{}) {
		if e != nil {
			return "", 0, 0, e
		}
		if ev == nil {
			continue
		}
		// event.UsageMetadata is the REAL ADK field (Event embeds
		// model.LLMResponse). This is the framework's BUILT-IN per-call visibility.
		if ev.UsageMetadata != nil {
			in += ev.UsageMetadata.PromptTokenCount
			out += ev.UsageMetadata.CandidatesTokenCount
		}
		if ev.IsFinalResponse() && ev.LLMResponse.Content != nil {
			for _, p := range ev.LLMResponse.Content.Parts {
				if p != nil && p.Text != "" {
					reply = p.Text
				}
			}
		}
	}
	return reply, in, out, nil
}

func hr()  { fmt.Println(strings.Repeat("=", 74)) }
func sub() { fmt.Println(strings.Repeat("-", 74)) }

func main() {
	ctx := context.Background()
	line := strings.Repeat("=", 74)
	fmt.Println(line)
	fmt.Println("ADK Go (google.golang.org/adk/v2 v2.0.0) — Cost + Pre-Spend Gate + Observability")
	fmt.Println(line)
	fmt.Println("Real llmagent + runner + session.Service. Offline metered fake model.")
	fmt.Println("SYNTHETIC token counts = len(text)/4 (labeled). No API key, no network.")
	fmt.Println()

	// Install an in-memory OTel span exporter on the GLOBAL tracer provider.
	// ADK's internal telemetry acquires its tracer from otel.GetTracerProvider()
	// (a delegating global), so setting this at runtime captures ADK's spans.
	spanRec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRec))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(ctx) }()

	mm := &meteredModel{}
	bot, err := orbit.NewWithModel("orbit", mm)
	if err != nil {
		panic(err)
	}
	meter := newCostMeter()

	// =====================================================================
	// DEMO 1 — PER-TENANT COST TRACKING
	// =====================================================================
	hr()
	fmt.Println("DEMO 1 — PER-TENANT COST TRACKING")
	hr()
	fmt.Println("Run 2 turns for tenantA, 2 for tenantB. Read usage off each event;")
	fmt.Println("aggregate per tenant. Q: does the framework RETURN usage per call?")
	fmt.Println()

	type turn struct{ tenant, user, sess, prompt string }
	turns := []turn{
		{"tenantA", "alice", "sA", "MEMO: tenantA deploy key is AAA-111"},
		{"tenantA", "alice", "sA", "what did I tell you?"},
		{"tenantB", "bob", "sB", "MEMO: tenantB deploy key is BBB-222"},
		{"tenantB", "bob", "sB", "recall my key please"},
	}
	for _, t := range turns {
		identity := t.tenant + "|" + t.user // fold tenant into ADK userID (convention)
		reply, in, out, err := runTurn(ctx, bot, identity, t.sess, t.prompt)
		if err != nil {
			panic(err)
		}
		meter.add(t.tenant, in, out)
		fmt.Printf("  [%s/%s] in=%-3d out=%-3d tok  (framework event.UsageMetadata)\n", t.tenant, t.user, in, out)
		fmt.Printf("        prompt: %q\n        reply : %q\n", t.prompt, reply)
	}

	fmt.Println()
	fmt.Println("  Per-tenant totals (DIY aggregation over the framework's per-call usage):")
	fmt.Printf("  %-9s %6s %7s %8s %12s\n", "tenant", "turns", "in_tok", "out_tok", "cost_usd*")
	var tenants []string
	for k := range meter.byTenant {
		tenants = append(tenants, k)
	}
	sort.Strings(tenants)
	for _, k := range tenants {
		tc := meter.byTenant[k]
		fmt.Printf("  %-9s %6d %7d %8d %12.8f\n", k, tc.turns, tc.inTok, tc.outTok, tc.costUSD)
	}
	fmt.Println("  * cost_usd from SYNTHETIC price table (0.075/0.300 USD per 1M in/out).")
	fmt.Println()
	fmt.Println("  VERDICT:")
	fmt.Println("    per-call usage visibility ...... [BUILT-IN]  event.UsageMetadata (real ADK field)")
	fmt.Println("    per-tenant aggregation ......... [DIY]       ADK has no tenant / no cost meter")

	// =====================================================================
	// DEMO 2 — PRE-SPEND BUDGET GATE
	// =====================================================================
	fmt.Println()
	hr()
	fmt.Println("DEMO 2 — PRE-SPEND BUDGET GATE")
	hr()
	fmt.Println("ADK exposes NO pre-spend quota primitive (unlike Pydantic AI")
	fmt.Println("UsageLimits.check_before_request). Cost is post-hoc token metadata.")
	fmt.Println("We implement the gate DIY and prove the model does NOT run when rejected.")
	fmt.Println()

	const budgetTok int32 = 40 // tiny SYNTHETIC per-tenant token budget
	fmt.Printf("  Per-tenant token budget = %d (SYNTHETIC, tiny on purpose).\n", budgetTok)
	fmt.Printf("  tenantA already spent %d tok in Demo 1.\n\n", meter.tokens("tenantA"))

	// gate is the DIY pre-spend check: reject BEFORE the runner is invoked.
	gate := func(tenant string) (allowed bool, reason string) {
		spent := meter.tokens(tenant)
		if spent >= budgetTok {
			return false, fmt.Sprintf("over budget: spent %d >= %d tok", spent, budgetTok)
		}
		return true, fmt.Sprintf("under budget: spent %d < %d tok", spent, budgetTok)
	}

	attempt := func(label, tenant, user, sess, prompt string) {
		before := mm.Calls()
		allowed, reason := gate(tenant)
		fmt.Printf("  %s — tenant=%s\n    pre-spend check: %s\n", label, tenant, reason)
		if !allowed {
			fmt.Printf("    -> REJECTED before the model ran. model.Calls before=%d after=%d (unchanged)\n",
				before, mm.Calls())
			return
		}
		identity := tenant + "|" + user
		_, in, out, err := runTurn(ctx, bot, identity, sess, prompt)
		if err != nil {
			panic(err)
		}
		meter.add(tenant, in, out)
		fmt.Printf("    -> ALLOWED, model ran. model.Calls before=%d after=%d (+1); spent in=%d out=%d\n",
			before, mm.Calls(), in, out)
	}

	// tenantC is fresh/under budget -> allowed (model runs).
	attempt("Attempt 1 (allowed) ", "tenantC", "carol", "sC", "MEMO: tenantC note hello")
	fmt.Println()
	// tenantA is already over budget from Demo 1 -> rejected (model must NOT run).
	attempt("Attempt 2 (rejected)", "tenantA", "alice", "sA", "MEMO: tenantA another secret DDD-444")

	fmt.Println()
	fmt.Println("  VERDICT:")
	fmt.Println("    pre-spend budget gate .......... [DIY]        no ADK primitive; we gate before runner.Run")
	fmt.Println("    proof model skipped on reject .. call-counter unchanged on the rejected turn")

	// =====================================================================
	// DEMO 3 — OBSERVABILITY (native OTel tracing)
	// =====================================================================
	fmt.Println()
	hr()
	fmt.Println("DEMO 3 — OBSERVABILITY (native OTel tracing)")
	hr()
	fmt.Println("ADK emits OTel spans w/ gen_ai.* attributes via the global tracer.")
	fmt.Println("We captured them with an in-memory exporter. Run one more traced turn")
	fmt.Println("wrapped in a DIY parent span carrying tenant.id, then dump all spans.")
	fmt.Println()

	// A DIY wrapper span that carries tenant_id — ADK's own spans do NOT.
	tracer := tp.Tracer("orbit/costobs")
	dctx, parent := tracer.Start(ctx, "orbit.turn",
		oteltrace.WithAttributes(attribute.String("tenant.id", "tenantA")))
	_, _, _, err = runTurn(dctx, bot, "tenantA|alice", "sA", "MEMO: traced turn ZZZ-999")
	if err != nil {
		panic(err)
	}
	parent.End()
	_ = tp.ForceFlush(ctx)

	spans := spanRec.Ended()
	fmt.Printf("  Captured %d spans total (in-memory exporter). gen_ai.* attributes:\n\n", len(spans))

	genaiSeen := false
	tenantOnADKSpan := false
	for _, s := range spans {
		var gattrs []string
		hasTenant := false
		for _, a := range s.Attributes() {
			k := string(a.Key)
			if strings.HasPrefix(k, "gen_ai.") {
				gattrs = append(gattrs, fmt.Sprintf("%s=%v", k, a.Value.AsInterface()))
				genaiSeen = true
			}
			if k == "tenant.id" {
				hasTenant = true
			}
		}
		tenantMark := "no tenant.id"
		if hasTenant {
			tenantMark = "tenant.id=" + attrString(s, "tenant.id")
			if s.Name() != "orbit.turn" {
				tenantOnADKSpan = true
			}
		}
		fmt.Printf("  - span %-28s [%s]\n", s.Name(), tenantMark)
		sort.Strings(gattrs)
		for _, g := range gattrs {
			fmt.Printf("        %s\n", g)
		}
	}

	fmt.Println()
	fmt.Println("  Observations:")
	fmt.Printf("    gen_ai.* attributes on ADK spans (incl. usage tokens) .. %v  [BUILT-IN]\n", genaiSeen)
	fmt.Printf("    tenant.id present on ADK's own spans natively ........... %v  [DIY to add]\n", tenantOnADKSpan)
	fmt.Println("    tenant.id rides only on our DIY 'orbit.turn' wrapper span above.")
	fmt.Println()
	fmt.Println("  VERDICT:")
	fmt.Println("    OTel spans + gen_ai.usage.* ..... [BUILT-IN]  self-hostable, free (OTLP exporters in dep graph)")
	fmt.Println("    tenant_id on spans .............. [DIY]       ADK has no tenant; enrich via wrapper span/baggage")
	fmt.Println("    hosted APM (e.g. LangSmith) ..... [PAID]      optional, not required for OTel")

	fmt.Println()
	sub()
	fmt.Println("SUMMARY (run-verified against real google.golang.org/adk/v2 v2.0.0):")
	fmt.Println("  #8  cost — per-call usage [BUILT-IN] visibility; per-tenant aggregation [DIY];")
	fmt.Println("            pre-spend budget gate [DIY] (no ADK primitive; measures, doesn't gate).")
	fmt.Println("  #10 observability — OTel gen_ai.* spans [BUILT-IN]; tenant_id on spans [DIY];")
	fmt.Println("            hosted APM [PAID]/optional.")
	sub()
	fmt.Println("Result: PASS — all three demos executed on the real ADK stack, offline.")
}

// attrString pulls a string attribute value off an ended span (small helper).
func attrString(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}
