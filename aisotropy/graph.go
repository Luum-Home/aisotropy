// Package aisotropy: the public multi-agent GRAPH composition surface (ADR-0060).
//
// A consumer composes a DAG where each NODE is a scoped agent (its own persona +
// scope set + tool subset + step budget) and each EDGE pipes one node's structured
// output into the input of the next. NewGraph() builds and validates the topology
// (cycles rejected at Build), WithGraph mounts it on an App, and App.RunGraph runs
// it in-process under a tenant-scoped context — no HTTP route in v1, matching
// App.Turn / App.RunTasks.
//
// The engine is a THIN NATIVE executor over internal/llm.RunToolAgent (each node
// is one RunToolAgent run), NOT the vendored gollem ext/graph or ext/team: those
// bypass the constitutional gates (the llm.ToolCaller/circuit-breaker/quota/
// scope-gate seams) and pull in a second persistence substrate ADR-0016 declined.
// Every node inherits the request's already-scoped ctx (derived, never re-minted),
// so no Node/Edge/PromptFunc a consumer writes can construct or widen a Scope —
// tenant isolation holds by construction, not by discipline.
//
// The public types here WRAP the kernel-internal internal/graph types (they are
// not raw re-exports of the executor), the same open-core discipline the other L2
// aliases use: a consumer works against this stable façade, the runtime owns the
// executor.
package aisotropy

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/graph"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tenancy"
)

// ToolCaller is the OPTIONAL capability a consumer's LLMClient MAY additionally
// implement so a graph node's Tools are driven through the provider's NATIVE
// tool-calling API (ADR-0012). A plain StreamChat-only LLMClient still runs a
// graph — its nodes just answer directly without calling tools. Re-exported so a
// consumer can supply a native-tool client through WithLLM and actually exercise
// a node's Tools from outside this module. The scope gate is unchanged: only a
// node's Permitted(Scopes) ∩ Tools are ever offered, and a non-offered tool name
// can never execute.
type ToolCaller = llm.ToolCaller

// ToolSpec is one tool offered to a native tool-calling model: its Name,
// Description, and optional JSON-Schema Parameters. Re-exported so a consumer's
// ToolCaller can name the slice element its CallTools receives.
type ToolSpec = llm.ToolSpec

// ToolCall is one tool invocation a native tool-calling model chose: the tool
// Name and its arguments (Args flattened, RawArgs the exact JSON). Re-exported so
// a consumer's ToolCaller returns them.
type ToolCall = llm.ToolCall

// ToolCallResult is what a ToolCaller.CallTools returns for one turn: the Calls
// the model wants executed (empty = answer directly) plus any free-text Message.
// Re-exported for consumer ToolCaller implementations.
type ToolCallResult = llm.ToolCallResult

// defaultGraphTenant / defaultGraphUser are the embedded single-tenant identity
// App.RunGraph scopes a graph to when the caller's ctx carries NO identity — the
// library-first default, exactly as an embedded app runs one tenant's workload.
// A multi-tenant embedder pre-identifies the ctx with WithGraphIdentity so each
// RunGraph observes its own tenant and never this default.
const (
	defaultGraphTenant = "default"
	defaultGraphUser   = "default"
	// graphSession is the fixed working-memory session key a graph run is scoped
	// to. A graph's nodes are stateless single-shot agents (no conversational
	// working-memory buffer to persist across runs), but identity validation
	// requires a non-empty session, so every graph run shares this sentinel — it
	// keeps the identity valid without implying a real conversation.
	graphSession = "graph"
)

// GraphInput is a graph's entry payload: the task that seeds every node's
// PromptFunc. Prompt is the user request; Data is an optional structured seed
// available to every node (not just the entry).
type GraphInput struct {
	Prompt string
	Data   map[string]any
}

// EdgePayload is the typed value an edge hands the destination node. Text feeds
// the destination's prompt; Data carries structured fields a PromptFunc can read.
type EdgePayload struct {
	Text string
	Data map[string]any
}

// AgentStep is one tool decision + result inside a node's run — the same contract
// the /chat/agent route returns, re-exported so a NodeResult's Steps are the
// genuine runtime type.
type AgentStep = api.AgentStep

// NodeResult is a node's structured output — exactly what an edge pipes
// downstream. Steps are the ordered tool decisions+results; Output is the node
// agent's final free-text answer (what a downstream node reads).
type NodeResult struct {
	NodeID string
	Steps  []AgentStep
	Output string
}

// PromptFunc builds a node's input prompt from the graph input and the mapped
// payloads of its completed in-edges (keyed by SOURCE node ID). It is FAIL-CLOSED:
// returning a non-nil error fails the node (and the graph) rather than run the
// agent on a bad input — a missing/malformed upstream payload never reaches the
// downstream agent.
type PromptFunc func(in GraphInput, from map[string]EdgePayload) (string, error)

// Node is a SCOPED AGENT: one agent run with its own persona (System), scope set
// (Scopes, ⊆ the request's scopes), tool subset (Tools, ⊆ Permitted(Scopes)), and
// per-node step budget (MaxSteps). Nodes never share a buffer; each runs under the
// request's derived, already-scoped ctx and can neither construct nor widen a
// Scope.
type Node struct {
	ID       string
	System   string
	Prompt   PromptFunc
	Scopes   []string
	Tools    []string
	MaxSteps int
}

// Edge is a directed dependency From -> To. Map transforms the source's
// NodeResult into the EdgePayload the destination receives; a nil Map passes
// {Text: src.Output} through. A non-nil error from Map FAILS CLOSED — the
// destination never runs on a malformed input.
type Edge struct {
	From, To string
	Map      func(NodeResult) (EdgePayload, error)
}

// GraphSpec is the builder returned by NewGraph. Build validates the topology
// once, up front, and freezes it into a Graph.
type GraphSpec struct {
	spec *graph.Spec
}

// Graph is a validated, frozen DAG ready to mount (WithGraph) and run (RunGraph).
type Graph struct {
	g *graph.Graph
}

// GraphResult carries every node's NodeResult (keyed by node ID) and the terminal
// (sink) node's Output as the graph's overall Output.
type GraphResult struct {
	Nodes  map[string]NodeResult
	Output string
}

// NewGraph returns an empty graph builder.
func NewGraph() *GraphSpec { return &GraphSpec{spec: graph.NewSpec()} }

// AddNode registers a node. It accumulates; duplicate IDs are rejected at Build.
func (s *GraphSpec) AddNode(n Node) *GraphSpec {
	s.spec.AddNode(toInternalNode(n))
	return s
}

// AddEdge registers a directed edge. Validated at Build (unknown/duplicate/self
// edges and cycles are rejected).
func (s *GraphSpec) AddEdge(e Edge) *GraphSpec {
	s.spec.AddEdge(toInternalEdge(e))
	return s
}

// SetEntry names the entry (seed) node. Validated at Build to exist and be a
// source (no in-edges).
func (s *GraphSpec) SetEntry(nodeID string) *GraphSpec {
	s.spec.SetEntry(nodeID)
	return s
}

// SetMaxNodes overrides the node-count ceiling (a cost/quota guard). <= 0 restores
// the default (graph.DefaultMaxNodes).
func (s *GraphSpec) SetMaxNodes(n int) *GraphSpec {
	s.spec.SetMaxNodes(n)
	return s
}

// Build validates the topology and freezes the spec into a Graph. It rejects
// cycles (DAG-only in v1), dangling/duplicate/self edges, unknown or duplicate
// node IDs, an entry that is not a source, and a node count over the ceiling. The
// node.Tools ⊆ Permitted(node.Scopes) gate needs the runtime's tool registry, so
// it is enforced at RunGraph time before any node runs (still up front relative to
// execution).
func (s *GraphSpec) Build() (*Graph, error) {
	g, err := graph.Build(s.spec)
	if err != nil {
		return nil, err
	}
	return &Graph{g: g}, nil
}

// WithGraph mounts a built graph on the App under a name (looked up by RunGraph).
// It accumulates like WithTool: call it once per graph. A nil graph or empty name
// is ignored.
func WithGraph(name string, g *Graph) Option {
	return func(a *App) {
		if name == "" || g == nil {
			return
		}
		if a.graphs == nil {
			a.graphs = map[string]*Graph{}
		}
		a.graphs[name] = g
	}
}

// WithGraphIdentity returns a copy of ctx carrying a validated (tenant, user)
// identity for a later App.RunGraph call — the "already-identified ctx" path a
// MULTI-TENANT embedder uses so each graph run is scoped to its own tenant. It
// injects identity into the runtime's ctx carriers exactly as the HTTP boundary
// does, so every node (and any tool executor it invokes) observes this tenant via
// TenantFromContext and can obtain only this tenant's Scope. An empty or invalid
// identity returns an error rather than a silently unscoped ctx. When RunGraph is
// called with a ctx that carries NO identity, it falls back to the single-tenant
// embedded default instead.
func WithGraphIdentity(ctx context.Context, tenant, user string) (context.Context, error) {
	ctx, err := api.WithIdentity(ctx, tenant, user, graphSession)
	if err != nil {
		return ctx, err
	}
	ctx = tenancy.WithTenant(ctx, tenant)
	ctx = tenancy.WithUser(ctx, user)
	return ctx, nil
}

// RunGraph executes a mounted graph in-process under a tenant-scoped ctx and
// returns each node's result plus the terminal node's Output. The runtime is
// built once, lazily, on first use (shared with App.Turn) and reused across runs.
//
// Identity: if ctx already carries an identity (from WithGraphIdentity) it is used
// as-is; otherwise the graph is scoped to the single-tenant embedded default. The
// scoped ctx is DERIVED into every node — the executor never mints a Scope, so no
// node can read or cross another tenant.
//
// Every node's LLM call routes through the runtime's protected (circuit-broken)
// client and the same tool registry + typed-dispatch the /chat/agent route uses,
// so the per-node scope gate, circuit breaker, and cost accounting all apply per
// node. Cancelling ctx stops in-flight nodes and unscheduled nodes.
func (a *App) RunGraph(ctx context.Context, name string, in GraphInput) (GraphResult, error) {
	g, ok := a.graphs[name]
	if !ok {
		return GraphResult{}, fmt.Errorf("aisotropy: no graph named %q is mounted (use WithGraph)", name)
	}

	rt, err := a.ensureRuntime()
	if err != nil {
		return GraphResult{}, err
	}

	// Scope the ctx: honor a caller-supplied identity (WithGraphIdentity), else the
	// embedded single-tenant default. Either way the ctx is validated and injected
	// into every runtime carrier before the first node runs.
	ctx, err = a.identifyGraphCtx(ctx)
	if err != nil {
		return GraphResult{}, err
	}

	typedExecutor := api.NewTypedToolExecutor(rt.typedHandlers)
	deps := graph.Deps{
		Client:   rt.client,
		Registry: rt.toolRegistry,
		Exec:     api.NewAgentToolExec(typedExecutor, rt.executor),
		Tracer:   rt.tracerProvider,
	}

	res, err := graph.Execute(ctx, deps, g.g, graph.GraphInput{Prompt: in.Prompt, Data: in.Data})
	if err != nil {
		return GraphResult{}, err
	}
	return toGraphResult(res), nil
}

// identifyGraphCtx returns ctx scoped to an identity: the one already on ctx (a
// multi-tenant caller used WithGraphIdentity), or the embedded single-tenant
// default. It re-injects the resolved identity into every carrier so all
// downstream readers agree.
func (a *App) identifyGraphCtx(ctx context.Context) (context.Context, error) {
	tenant, ok := api.TenantFromContext(ctx)
	if !ok || tenant == "" {
		tenant = defaultGraphTenant
	}
	user, ok := api.UserFromContext(ctx)
	if !ok || user == "" {
		user = defaultGraphUser
	}
	return WithGraphIdentity(ctx, tenant, user)
}

// toInternalNode wraps a public Node into the internal graph.Node, adapting the
// public PromptFunc (public payload types) to the internal one.
func toInternalNode(n Node) graph.Node {
	var pf graph.PromptFunc
	if n.Prompt != nil {
		userPrompt := n.Prompt
		pf = func(in graph.GraphInput, from map[string]graph.EdgePayload) (string, error) {
			return userPrompt(fromInternalInput(in), fromInternalPayloads(from))
		}
	}
	return graph.Node{
		ID:       n.ID,
		System:   n.System,
		Prompt:   pf,
		Scopes:   n.Scopes,
		Tools:    n.Tools,
		MaxSteps: n.MaxSteps,
	}
}

// toInternalEdge wraps a public Edge, adapting the public Map (public NodeResult
// -> public EdgePayload) to the internal one.
func toInternalEdge(e Edge) graph.Edge {
	var mp func(graph.NodeResult) (graph.EdgePayload, error)
	if e.Map != nil {
		userMap := e.Map
		mp = func(nr graph.NodeResult) (graph.EdgePayload, error) {
			p, err := userMap(fromInternalNodeResult(nr))
			if err != nil {
				return graph.EdgePayload{}, err
			}
			return graph.EdgePayload{Text: p.Text, Data: p.Data}, nil
		}
	}
	return graph.Edge{From: e.From, To: e.To, Map: mp}
}

func fromInternalInput(in graph.GraphInput) GraphInput {
	return GraphInput{Prompt: in.Prompt, Data: in.Data}
}

func fromInternalPayloads(from map[string]graph.EdgePayload) map[string]EdgePayload {
	out := make(map[string]EdgePayload, len(from))
	for k, v := range from {
		out[k] = EdgePayload{Text: v.Text, Data: v.Data}
	}
	return out
}

func fromInternalNodeResult(nr graph.NodeResult) NodeResult {
	steps := make([]AgentStep, 0, len(nr.Steps))
	for _, s := range nr.Steps {
		steps = append(steps, AgentStep{Tool: s.Tool, Args: s.Args, Result: s.Result})
	}
	return NodeResult{NodeID: nr.NodeID, Steps: steps, Output: nr.Output}
}

func toGraphResult(res graph.Result) GraphResult {
	nodes := make(map[string]NodeResult, len(res.Nodes))
	for id, nr := range res.Nodes {
		nodes[id] = fromInternalNodeResult(nr)
	}
	return GraphResult{Nodes: nodes, Output: res.Output}
}
