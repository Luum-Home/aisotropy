package graph

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// maxDefaultConcurrency caps the auto-chosen worker count, mirroring
// taskagent.RunTasks: each concurrent node holds a live LLM stream, so an
// unbounded fan-out over a many-core host would swamp the provider rate limit /
// circuit breaker. NumCPU, clamped here, is the default.
const maxDefaultConcurrency = 8

// Deps are the runtime handles the executor needs, injected by the aisotropy
// façade (which owns the higher-layer wiring). Passing them in — rather than
// importing internal/api — keeps internal/graph below the api/façade layer.
type Deps struct {
	// Client is the runtime's protected (circuit-broken) LLM client. When it
	// implements llm.ToolCaller each node drives the provider's NATIVE tool-calling
	// through RunToolAgent; a plain streaming client still runs (the model answers
	// directly, no tool calls). Every node's call goes through this one client, so
	// the per-tenant quota gate and circuit breaker apply per node (ADR-0060 §5).
	Client llm.Client
	// Registry is the scope-gated tool registry. A node is offered only
	// Permitted(node.Scopes) ∩ node.Tools; a hallucinated/non-offered tool can
	// never execute (the RunToolAgent structural scope-gate).
	Registry *tools.Registry
	// Exec is the raw-JSON tool-dispatch closure (typed reflection + untyped map
	// executor), built once by the façade via api.NewAgentToolExec so a node's tool
	// dispatch is byte-for-byte the /chat/agent route's.
	Exec func(ctx context.Context, name, argsJSON string) (string, error)
	// Tracer instruments each node's agent with OTel spans; may be nil.
	Tracer oteltrace.TracerProvider
	// Concurrency bounds how many nodes run at once; <= 0 selects
	// min(NumCPU, maxDefaultConcurrency), at least 1.
	Concurrency int
}

// Result is the executor's output: every node's NodeResult keyed by node ID, and
// the terminal (sink) node's Output as the graph's overall Output.
type Result struct {
	Nodes  map[string]NodeResult
	Output string
}

// Execute runs g under ctx and returns each node's result plus the terminal
// node's output. It is the layered concurrent scheduler ADR-0060 specifies:
//
//   - Scope gate FIRST (fail closed, before ANY node runs): every node's
//     node.Tools ⊆ Permitted(node.Scopes) is verified against the live registry;
//     a node listing a tool its scopes do not grant fails the whole graph up front.
//   - Dataflow scheduling: a node runs as soon as ALL its in-edges have resolved,
//     so a diamond join waits for every parent. Independent nodes run concurrently,
//     bounded by Deps.Concurrency (the taskagent.RunTasks slot-limit shape).
//   - Edges pipe output→input: on a node's completion each out-Edge.Map transforms
//     its NodeResult into the destination's EdgePayload (nil Map ⇒ {Text: Output}).
//   - FAIL CLOSED: a PromptFunc error, an Edge.Map error, or a node's own LLM error
//     fails the graph — the ctx is cancelled, in-flight nodes stop, and any
//     downstream node whose input never fully resolved is NEVER started (so a
//     malformed upstream output never reaches a downstream agent).
//   - Cancellation: cancelling ctx stops in-flight nodes (RunToolAgent honors ctx)
//     and prevents new ones; Execute returns ctx.Err().
//
// nodeCtx is DERIVED from ctx (context.WithCancel only), never re-minted, so the
// request's tenant identity is inherited by every node and cannot be widened.
func Execute(ctx context.Context, deps Deps, g *Graph, input GraphInput) (Result, error) {
	// Scope gate, up front: reject a node whose Tools are not all permitted by its
	// Scopes before spending a single token (ADR-0060 §3).
	for _, id := range g.order {
		if err := checkNodeScopes(deps.Registry, g.nodes[id]); err != nil {
			return Result{}, err
		}
	}

	conc := deps.Concurrency
	if conc <= 0 {
		conc = defaultConcurrency()
	}

	parent := ctx
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		mu       sync.Mutex
		results  = make(map[string]NodeResult, len(g.nodes))
		inbox    = make(map[string]map[string]EdgePayload, len(g.nodes))
		indeg    = make(map[string]int, len(g.nodes))
		firstErr error
	)
	for _, id := range g.order {
		inbox[id] = map[string]EdgePayload{}
		indeg[id] = len(g.inEdges[id])
	}

	// sem bounds concurrent node bodies. A goroutine acquires a slot only around
	// runNode and RELEASES it before scheduling children — so no goroutine ever
	// holds a slot while spawning more work, which is what keeps a graph wider than
	// the slot limit deadlock-free (errgroup.SetLimit + recursive Go would block).
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel() // stop in-flight nodes and gate out any not-yet-started node
	}

	var launch func(id string)
	launch = func(id string) {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// A failed/cancelled graph must not start new nodes.
			if ctx.Err() != nil {
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}

			mu.Lock()
			from := make(map[string]EdgePayload, len(inbox[id]))
			for k, v := range inbox[id] {
				from[k] = v
			}
			mu.Unlock()

			nr, err := runNode(ctx, deps, g.nodes[id], input, from)
			<-sem // release BEFORE scheduling children

			if err != nil {
				fail(fmt.Errorf("node %q: %w", id, err))
				return
			}

			mu.Lock()
			results[id] = nr
			var ready []string
			var mapErr error
			for _, e := range g.outEdges[id] {
				payload, merr := applyEdge(e, nr)
				if merr != nil {
					mapErr = fmt.Errorf("edge %q->%q map: %w", e.From, e.To, merr)
					break
				}
				inbox[e.To][id] = payload
				indeg[e.To]--
				if indeg[e.To] == 0 {
					ready = append(ready, e.To)
				}
			}
			mu.Unlock()

			if mapErr != nil {
				fail(mapErr)
				return
			}
			for _, r := range ready {
				launch(r)
			}
		}()
	}

	// Collect the sources (in-degree 0) BEFORE launching any of them: a launched
	// node's goroutine mutates indeg for its dependents, so reading indeg while
	// launching would race that write. Snapshot first, then launch.
	var sources []string
	for _, id := range g.order {
		if indeg[id] == 0 {
			sources = append(sources, id)
		}
	}
	for _, id := range sources {
		launch(id)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if perr := parent.Err(); perr != nil {
		// External cancellation stops the whole graph and surfaces ctx's error,
		// like RunTasks' whole-batch cancellation contract — checked BEFORE firstErr
		// so a cancellation-induced node error does not mask the real cause.
		return Result{}, perr
	}
	if firstErr != nil {
		return Result{}, firstErr
	}
	return Result{Nodes: results, Output: results[g.Terminal()].Output}, nil
}

// runNode runs one node: assemble its prompt (fail closed on a PromptFunc error),
// compute its scope-gated tool specs, and drive one RunToolAgentWithOutput. ctx is
// the derived, already-scoped request ctx — identity is inherited, never re-minted.
func runNode(ctx context.Context, deps Deps, node Node, input GraphInput, from map[string]EdgePayload) (NodeResult, error) {
	prompt, err := buildPrompt(node, input, from)
	if err != nil {
		// FAIL CLOSED: a PromptFunc that cannot assemble a valid input (e.g. a
		// missing/empty upstream payload) fails the node — the agent never runs on
		// a malformed input.
		return NodeResult{}, fmt.Errorf("prompt: %w", err)
	}
	specs := nodeSpecs(deps.Registry, node)
	steps, output, err := llm.RunToolAgentWithOutput(ctx, deps.Client, deps.Tracer, node.System, prompt, specs, deps.Exec, node.MaxSteps)
	if err != nil {
		return NodeResult{}, err
	}
	return NodeResult{NodeID: node.ID, Steps: steps, Output: output}, nil
}

// buildPrompt resolves a node's input prompt. A nil PromptFunc defaults to the
// graph input's Prompt (the entry seed); otherwise the node's PromptFunc maps the
// input + its in-edge payloads.
func buildPrompt(node Node, input GraphInput, from map[string]EdgePayload) (string, error) {
	if node.Prompt == nil {
		return input.Prompt, nil
	}
	return node.Prompt(input, from)
}

// applyEdge maps a source node's result into the destination's payload. A nil Map
// passes the source's final text through as {Text: Output}.
func applyEdge(e Edge, src NodeResult) (EdgePayload, error) {
	if e.Map == nil {
		return EdgePayload{Text: src.Output}, nil
	}
	return e.Map(src)
}

// nodeSpecs is the double scope-gate: it offers only the tools that are BOTH
// permitted by the node's scopes AND listed in node.Tools, carrying each tool's
// argument schema so a typed tool advertises its real shape. Only these specs are
// registered as function tools, so a non-offered tool name can never execute.
func nodeSpecs(reg *tools.Registry, node Node) []llm.ToolSpec {
	if reg == nil || len(node.Tools) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(node.Tools))
	for _, t := range node.Tools {
		want[t] = struct{}{}
	}
	permitted := reg.Permitted(node.Scopes)
	specs := make([]llm.ToolSpec, 0, len(node.Tools))
	for _, t := range permitted {
		if _, ok := want[t.Name]; ok {
			specs = append(specs, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		}
	}
	return specs
}

// checkNodeScopes enforces node.Tools ⊆ Permitted(node.Scopes) against the live
// registry. A node listing a tool its scopes do not grant (or any tool when there
// is no registry) is rejected — the scope-widening path is closed at the door.
func checkNodeScopes(reg *tools.Registry, node Node) error {
	if len(node.Tools) == 0 {
		return nil
	}
	have := make(map[string]struct{})
	if reg != nil {
		for _, t := range reg.Permitted(node.Scopes) {
			have[t.Name] = struct{}{}
		}
	}
	for _, name := range node.Tools {
		if _, ok := have[name]; !ok {
			return fmt.Errorf("graph: node %q lists tool %q not permitted by its scopes %v", node.ID, name, node.Scopes)
		}
	}
	return nil
}

// defaultConcurrency is min(NumCPU, maxDefaultConcurrency), at least 1.
func defaultConcurrency() int {
	n := runtime.NumCPU()
	if n > maxDefaultConcurrency {
		n = maxDefaultConcurrency
	}
	if n < 1 {
		n = 1
	}
	return n
}
