// strengths_demo — a RUN-VERIFIED, OFFLINE steelman of what Firebase Genkit Go
// (github.com/firebase/genkit/go v1.10.0, Apache-2.0) is GENUINELY GOOD AT.
//
// Run:  go run ./cmd/strengths      (from arenas/genkit-go)   -> exit 0
//
// The rest of this arena documents the ONE narrow axis where aisotropy differs
// (isolation-by-default; Genkit's memory primitive is experimental and
// tenant-unaware). This file is the other side of the ledger: three real
// framework strengths, each EXERCISED against the genuine v1.10.0 API with a
// registered offline fake model (no API key, no network). For each it prints:
//
//	STRENGTH <n> <name>: <one-line evidence>
//
// HONESTY CONTRACT: every strength drives the framework's OWN API (cited by
// package/symbol) and proves it ran. Genkit's flow + dotprompt + provider-
// abstraction DX is a real strength (Firebase/Google backing, deployable
// units, prompt-as-config); aisotropy is a self-hosted multi-tenant Go runtime
// — a DIFFERENT niche. Both statements are true and this demo says so.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"genkit-go-arena/orbit"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func main() {
	ctx := context.Background()

	fmt.Println(strings.Repeat("=", 96))
	fmt.Println("GENKIT GO — STEELMAN: what the framework is genuinely great at (REAL v1.10.0, offline)")
	fmt.Println("framework: github.com/firebase/genkit/go v1.10.0   models: fake (no network, no API key)")
	fmt.Println(strings.Repeat("=", 96))

	// orbit.New already does the real genkit.Init (registry + WithPromptFS +
	// WithDefaultModel) and registers the offline fake model and the
	// orbit_support.prompt dotprompt file.
	app, err := orbit.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit init failed:", err)
		os.Exit(2)
	}

	// One OTel exporter attached to Genkit's OWN provider (genkit ships the
	// tracing.TracerProvider). Strength 1 reads back the spans it produced.
	exporter := tracetest.NewInMemoryExporter()
	tracing.TracerProvider().RegisterSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter))

	ok := true
	ok = strengthFlows(ctx, app, exporter) && ok
	ok = strengthDotprompt(ctx, app) && ok
	ok = strengthProviderAbstraction(ctx, app) && ok

	fmt.Println("\n" + strings.Repeat("=", 96))
	fmt.Println("Honest framing: Genkit Go is a strong, general-purpose AI application library. Its flow")
	fmt.Println("model (observable/deployable units), dotprompt (prompt-as-config with schemas), and")
	fmt.Println("model-agnostic provider abstraction are genuinely excellent DX — backed by Firebase/Google.")
	fmt.Println("aisotropy is a self-hosted MULTI-TENANT Go runtime: a different niche. The one axis where")
	fmt.Println("they diverge is isolation-by-default — Genkit's memory primitive is experimental and")
	fmt.Println("tenant-unaware (see README 'Isolation model'), so tenant binding is on the developer.")
	fmt.Println(strings.Repeat("=", 96))

	if ok {
		fmt.Println("\nResult: PASS — all three strengths exercised against the real framework.")
		os.Exit(0)
	}
	fmt.Println("\nResult: FAIL — a strength did not reproduce; see above.")
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// STRENGTH 1 — Flows: observable, composable, individually-runnable units.
//
// API: genkit.DefineFlow / genkit.DefineStreamingFlow (genkit/genkit.go),
//      *core.Flow[In,Out,Stream].Run / .Stream (core/flow.go).
// A flow is a named unit Genkit can trace, expose in the Dev UI, and deploy as
// an HTTP handler. Observability is automatic: running one emits OTel spans on
// genkit's own TracerProvider with zero extra wiring.
// ---------------------------------------------------------------------------

func strengthFlows(ctx context.Context, app *orbit.Orbit, exporter *tracetest.InMemoryExporter) bool {
	hdr("STRENGTH 1 — Flows (observable, composable, deployable units)")

	// Two named flows; the SECOND composes the FIRST by calling .Run — flows are
	// ordinary composable functions the framework wraps with tracing.
	classify := genkit.DefineFlow(app.G, "steelman.classify",
		func(ctx context.Context, text string) (string, error) {
			// Reuse the app's real structured-output classify path underneath.
			cat, err := app.Classify(ctx, text)
			if err != nil {
				return "", err
			}
			return cat.Category, nil
		})

	triage := genkit.DefineFlow(app.G, "steelman.triage",
		func(ctx context.Context, ticket string) (string, error) {
			cat, err := classify.Run(ctx, ticket) // <- flow composes a flow
			if err != nil {
				return "", err
			}
			return "routed '" + trunc(ticket, 28) + "' -> queue:" + cat, nil
		})

	exporter.Reset()
	routed, err := triage.Run(ctx, "I want a refund, you charged me twice")
	_ = tracing.TracerProvider().ForceFlush(ctx)
	if err != nil {
		fmt.Println("  flow error:", err)
		return false
	}

	// Observability comes free: count spans emitted by the two nested flows.
	spans := exporter.GetSpans()
	flowSpans := 0
	for _, s := range spans {
		if strings.Contains(s.Name, "steelman.") || s.Name == "orbitClassify" {
			flowSpans++
		}
	}
	composed := strings.Contains(routed, "queue:billing")

	// Streaming flow: same DefineStreamingFlow primitive yields progressive output.
	stream := genkit.DefineStreamingFlow(app.G, "steelman.stream",
		func(ctx context.Context, n int, cb core.StreamCallback[string]) (string, error) {
			for i := 1; i <= n; i++ {
				if cb != nil {
					if err := cb(ctx, fmt.Sprintf("chunk-%d", i)); err != nil {
						return "", err
					}
				}
			}
			return fmt.Sprintf("done:%d", n), nil
		})
	streamed := 0
	var streamDone string
	for val, err := range stream.Stream(ctx, 4) {
		if err != nil {
			fmt.Println("  stream error:", err)
			return false
		}
		if val.Done {
			streamDone = val.Output
		} else {
			streamed++
		}
	}

	pass := composed && flowSpans >= 2 && streamed == 4 && streamDone == "done:4"
	fmt.Printf("  %s composed flow ran (%q); %d flow spans auto-emitted; streaming flow delivered %d chunks + %q\n",
		mark(pass), routed, flowSpans, streamed, streamDone)
	fmt.Println("  API: genkit.DefineFlow / DefineStreamingFlow + Flow.Run/Flow.Stream — traceable, Dev-UI-visible, deployable.")
	return pass
}

// ---------------------------------------------------------------------------
// STRENGTH 2 — Dotprompt: prompt-as-config with a typed input schema.
//
// API: genkit.LookupPrompt (genkit/genkit.go) + ai.Prompt.Execute /
//      ai.WithInput (ai/generate.go). The model, template, and input schema
//      live in orbit/prompts/orbit_support.prompt — NOT in Go source. Editing
//      the prompt or its schema needs no recompile: this is the config/code
//      separation Genkit is genuinely good at.
// ---------------------------------------------------------------------------

func strengthDotprompt(ctx context.Context, app *orbit.Orbit) bool {
	hdr("STRENGTH 2 — Dotprompt (prompt-as-config, decoupled from code)")

	// The .prompt file was registered by orbit.New via WithPromptFS. Look it up
	// by name from the registry — pure config, no Go literal.
	p := genkit.LookupPrompt(app.G, "orbit_support")
	if p == nil {
		fmt.Println("  dotprompt 'orbit_support' not registered")
		return false
	}

	// Execute it with structured input matching the file's declared schema
	// (product: string, question: string). The framework renders the handlebars
	// template and drives a real generation through the registry model.
	resp, err := p.Execute(ctx,
		ai.WithModel(app.Model),
		ai.WithInput(map[string]any{"product": "Orbit Pro", "question": "hello"}),
	)
	if err != nil {
		fmt.Println("  dotprompt execute error:", err)
		return false
	}
	ran := strings.TrimSpace(resp.Text()) != ""

	// Second execution with DIFFERENT input through the SAME .prompt — proving
	// the template is data-driven config, not a hardcoded string.
	resp2, err := p.Execute(ctx,
		ai.WithModel(app.Model),
		ai.WithInput(map[string]any{"product": "Orbit Lite", "question": "where is my order #4821?"}),
	)
	reran := err == nil && strings.TrimSpace(resp2.Text()) != ""

	pass := ran && reran
	fmt.Printf("  %s executed orbit_support.prompt with two different input structs -> %q / %q\n",
		mark(pass), trunc(resp.Text(), 30), trunc(resp2.Text(), 30))
	fmt.Println("  API: genkit.LookupPrompt + Prompt.Execute + ai.WithInput — model+template+input-schema live in a .prompt file (hot-editable, no recompile).")
	return pass
}

// ---------------------------------------------------------------------------
// STRENGTH 3 — Model-agnostic provider abstraction.
//
// API: genkit.DefineModel / genkit.LookupModel (genkit/genkit.go) + the
//      ai.Model interface + ai.WithModelName (ai/generate.go). Every provider
//      (Gemini, OpenAI, Anthropic, Ollama, ...) is registered under a name and
//      selected by string; the CALLER CODE is identical across providers. Here
//      two distinct fake "providers" stand in — the generate call differs ONLY
//      by the model name string.
// ---------------------------------------------------------------------------

func strengthProviderAbstraction(ctx context.Context, app *orbit.Orbit) bool {
	hdr("STRENGTH 3 — Model-agnostic provider abstraction (swap by name)")

	// Register two distinct "providers" on the real registry. In production these
	// would be plugins/googlegenai, plugins/openai, plugins/anthropic, plugins/ollama.
	defProvider := func(name, banner string) {
		genkit.DefineModel(app.G, name,
			&ai.ModelOptions{Label: name, Supports: &ai.ModelSupports{SystemRole: true, Multiturn: true}},
			func(_ context.Context, _ *ai.ModelRequest, _ core.StreamCallback[*ai.ModelResponseChunk]) (*ai.ModelResponse, error) {
				return &ai.ModelResponse{
					Message:      ai.NewMessage(ai.RoleModel, nil, ai.NewTextPart(banner)),
					FinishReason: ai.FinishReasonStop,
				}, nil
			})
	}
	defProvider("provider-fast/chat", "[fast] refunds take 5-7 business days")
	defProvider("provider-smart/chat", "[smart] Refunds are issued to the original card within five to seven business days.")

	// The registry resolves a provider by name (ai.Model interface) — the
	// framework's abstraction seam.
	if genkit.LookupModel(app.G, "provider-fast/chat") == nil ||
		genkit.LookupModel(app.G, "provider-smart/chat") == nil {
		fmt.Println("  provider not resolvable via genkit.LookupModel")
		return false
	}

	// IDENTICAL caller code; only the model-name string changes.
	ask := func(provider string) string {
		resp, err := genkit.Generate(ctx, app.G,
			ai.WithModelName(provider),
			ai.WithPrompt("How long do refunds take?"),
		)
		if err != nil {
			return ""
		}
		return resp.Text()
	}
	fast := ask("provider-fast/chat")
	smart := ask("provider-smart/chat")

	pass := strings.Contains(fast, "[fast]") && strings.Contains(smart, "[smart]") && fast != smart
	fmt.Printf("  %s same genkit.Generate call, only ai.WithModelName changed -> fast=%q  smart=%q\n",
		mark(pass), trunc(fast, 26), trunc(smart, 34))
	fmt.Println("  API: genkit.DefineModel/LookupModel + ai.Model + ai.WithModelName — provider is a registry lookup; caller code is provider-independent.")
	return pass
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hdr(s string) {
	fmt.Println("\n" + strings.Repeat("-", 96))
	fmt.Println("  " + s)
	fmt.Println(strings.Repeat("-", 96))
}

func mark(ok bool) string {
	if ok {
		return "✓" // check
	}
	return "✗" // cross
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
