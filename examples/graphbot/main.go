// Command graphbot is the smallest MULTI-AGENT graph you can build on Aisotropy
// (ADR-0060): a researcher → writer → reviewer DAG where each NODE is a scoped
// agent (its own persona + scope set + tool subset + step budget) and each EDGE
// pipes one node's structured output into the input of the next.
//
// It is a SEPARATE Go module (examples/graphbot/go.mod) with a replace directive
// pointing at the repo root, and it imports ONLY the public façade package
// github.com/Luum-Home/aisotropy/aisotropy — never any private package. Because it
// is a distinct module, Go's own internal-package rule PHYSICALLY forbids it from
// reaching into aisotropy/internal/...: the fact that it compiles is the "public
// surface only" guarantee, and a test proves an L2 node cannot obtain a Scope
// (the Phase 9 DoD).
//
// The graph is a diamond: researcher feeds BOTH writer and reviewer, and writer
// also feeds reviewer, so the reviewer waits for BOTH parents (a diamond join).
// The researcher is offered one scope-gated tool (web_search); the writer and
// reviewer are given NO tools and cannot escalate. Each node inherits the run's
// already-scoped context.Context — no node can construct or widen a tenant Scope.
//
// Run it keyless (no API key, no Docker) — see README.md:
//
//	go run .
//
// With the keyless stub LLM the nodes answer directly (the stub is not a native
// tool-caller), so this prints a composed, deterministic walk of the graph rather
// than real model output; point WithLLM at a real provider (or a ToolCaller) to
// see genuine researcher tool use and writer/reviewer prose.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	// NODE 1 — researcher: a scoped agent with the web_search tool. It seeds off the
	// graph input's Prompt.
	researcher := aisotropy.Node{
		ID:       "researcher",
		System:   "You research a topic and return terse bullet findings with sources.",
		Scopes:   []string{"tools:search"},
		Tools:    []string{"web_search"},
		MaxSteps: 6,
		Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
			return "Research this topic and list key findings:\n" + in.Prompt, nil
		},
	}

	// NODE 2 — writer: NO tools (pure composition). It reads the researcher's mapped
	// payload and FAILS CLOSED if the findings are missing — the agent never runs on
	// a half-built input.
	writer := aisotropy.Node{
		ID:       "writer",
		System:   "You write a clear 3-paragraph brief from the supplied findings. No new claims.",
		MaxSteps: 2,
		Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
			f, ok := from["researcher"]
			if !ok || f.Text == "" {
				return "", fmt.Errorf("writer: missing researcher findings")
			}
			return "Write a brief from these findings:\n" + f.Text, nil
		},
	}

	// NODE 3 — reviewer: NO tools. The diamond sink: it sees BOTH the researcher's
	// findings and the writer's brief and returns a verdict.
	reviewer := aisotropy.Node{
		ID:       "reviewer",
		System:   "You fact-check the brief against the findings and return APPROVED or a fix list.",
		MaxSteps: 2,
		Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
			return "Findings:\n" + from["researcher"].Text + "\n\nBrief:\n" + from["writer"].Text, nil
		},
	}

	// Build validates the topology once, up front: a cycle here would be rejected.
	g, err := aisotropy.NewGraph().
		AddNode(researcher).AddNode(writer).AddNode(reviewer).
		AddEdge(aisotropy.Edge{From: "researcher", To: "writer"}).
		AddEdge(aisotropy.Edge{From: "researcher", To: "reviewer"}). // diamond: reviewer sees both
		AddEdge(aisotropy.Edge{From: "writer", To: "reviewer"}).
		SetEntry("researcher").
		Build()
	if err != nil {
		log.Fatalf("graph build failed: %v", err)
	}

	app := aisotropy.New(
		// A scope-gated tool the researcher may call. The writer and reviewer declare
		// no tools, so the model is never even shown one for them.
		aisotropy.WithTool(aisotropy.Tool{
			Name:          "web_search",
			Description:   "search the web for a query",
			RequiredScope: "tools:search",
		}),
		// The host implementation of the tool. A real bot calls a search API here;
		// it reads the calling tenant from ctx (read-only, never constructed).
		aisotropy.WithToolExecutor(searchExecutor{}),
		// Mount the graph under a name RunGraph looks up.
		aisotropy.WithGraph("brief", g),
		// Keyless boot: with no ANTHROPIC_API_KEY the credential-less stub drives the
		// nodes so this example runs out of the box.
	)
	defer func() { _ = app.Close() }()

	// RunGraph executes the graph in-process under a scoped ctx. With no identity on
	// ctx it uses the single-tenant embedded default; a multi-tenant embedder would
	// pre-scope with aisotropy.WithGraphIdentity(ctx, tenant, user).
	res, err := app.RunGraph(context.Background(), "brief", aisotropy.GraphInput{
		Prompt: "the impact of caching on LLM serving latency",
	})
	if err != nil {
		log.Fatalf("RunGraph failed: %v", err)
	}

	fmt.Println("=== researcher ===")
	fmt.Println(res.Nodes["researcher"].Output)
	fmt.Printf("(tool steps: %d)\n\n", len(res.Nodes["researcher"].Steps))
	fmt.Println("=== writer ===")
	fmt.Println(res.Nodes["writer"].Output)
	fmt.Println()
	fmt.Println("=== reviewer (terminal / graph output) ===")
	fmt.Println(res.Output)
}

// searchExecutor is the host tool implementation. It reads the calling tenant from
// ctx (the only identity a consumer may access — read-only, never constructed) and
// returns a canned result, demonstrating tenant-aware tool logic without ever
// touching an internal memory scope.
type searchExecutor struct{}

func (searchExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	return fmt.Sprintf("[tenant %q] %s(%v): cache hit rate dominates tail latency; batch size matters", tenant, name, args), nil
}
