// Package graph is the kernel-internal executor for ADR-0060 multi-agent graph
// orchestration: a thin native DAG scheduler where each NODE is one scoped
// llm.RunToolAgent run (its own persona + scope set + tool subset + step budget)
// and each EDGE pipes one node's structured output into the input of the next.
//
// It is deliberately NOT the vendored gollem ext/graph or ext/team engine (see
// ADR-0060 "Engine decision"): those bypass the aisotropy seams the
// constitutional gates live in (the llm.ToolCaller/breaker/quota/scope-gate) and
// pull in a second persistence substrate ADR-0016 declined. This executor calls
// RunToolAgent per node, threads the request's already-scoped context.Context
// (derived, never re-minted) into every node, and reuses the taskagent.RunTasks
// slot-limited concurrency shape — so it inherits every gate for free and adds
// only the one missing abstraction: the edge.
//
// Layering: this package depends on internal/llm (the node body) and
// internal/tools (the scope-gated registry) only. The typed-tool dispatch closure
// and identity injection live in the aisotropy façade / internal/api, which are
// HIGHER layers — they are injected here as Deps values, so internal/graph never
// imports internal/api.
package graph

import (
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// DefaultMaxNodes bounds a graph's node count when a Spec does not set its own
// ceiling. The ceiling is a constitutional cost/quota guard (ADR-0060 §5): a
// graph's worst-case spend is node-count × per-node MaxSteps × per-turn cost, so
// an unbounded node count would let a composed graph fan out past the token
// budget. 32 is the ADR's suggested default.
const DefaultMaxNodes = 32

// GraphInput is the graph's entry payload: the request that seeds every node's
// PromptFunc. Prompt is the user task; Data is an optional structured seed
// available to every node (not just the entry) so a PromptFunc can read shared
// request-level fields.
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

// NodeResult is a node's structured output — exactly what an edge pipes
// downstream. Steps are the ordered tool decisions+results the node's agent made;
// Output is the agent's final free-text answer (what a downstream node reads).
type NodeResult struct {
	NodeID string
	Steps  []llm.ToolStep
	Output string
}

// PromptFunc builds a node's input prompt from the graph input and the mapped
// payloads of its completed in-edges (keyed by SOURCE node ID). It is FAIL-CLOSED:
// returning a non-nil error fails the node (and the graph) rather than run the
// agent on a bad input — a malformed/missing upstream payload never reaches the
// downstream agent (ADR-0060 §4).
type PromptFunc func(in GraphInput, from map[string]EdgePayload) (string, error)

// Node is a SCOPED AGENT: one RunToolAgent run with its own persona (System),
// scope set (Scopes), tool subset (Tools), and per-node step budget (MaxSteps).
// Nodes never share a buffer; each runs under the request's derived, already-
// scoped ctx and can neither construct nor widen a Scope (the ADR-0016 invariant).
type Node struct {
	ID       string     // unique within the graph
	System   string     // this node's persona / system prompt
	Prompt   PromptFunc // maps graph input + in-edge payloads -> this node's prompt
	Scopes   []string   // the ONLY scopes this node's agent runs under (⊆ request scopes)
	Tools    []string   // registered tool names this node may call (⊆ Permitted(Scopes))
	MaxSteps int        // per-node turn budget (bounds spend)
}

// Edge is a directed dependency From -> To. Map transforms the source's
// NodeResult into the EdgePayload the destination receives; a nil Map passes
// {Text: src.Output} through. A non-nil error from Map FAILS CLOSED — the
// destination never runs on a malformed input.
type Edge struct {
	From, To string
	Map      func(NodeResult) (EdgePayload, error)
}

// Spec is the mutable builder. Build validates the topology once, up front, and
// freezes it into a Graph.
type Spec struct {
	nodes    []Node
	edges    []Edge
	entry    string
	maxNodes int
}

// NewSpec returns an empty builder with the default node ceiling.
func NewSpec() *Spec { return &Spec{maxNodes: DefaultMaxNodes} }

// AddNode appends a node. Duplicate IDs are rejected at Build, not here, so the
// builder stays a pure accumulator.
func (s *Spec) AddNode(n Node) *Spec { s.nodes = append(s.nodes, n); return s }

// AddEdge appends a directed edge. Validated at Build.
func (s *Spec) AddEdge(e Edge) *Spec { s.edges = append(s.edges, e); return s }

// SetEntry names the entry (seed) node. Validated at Build to exist and to be a
// source (no in-edges).
func (s *Spec) SetEntry(id string) *Spec { s.entry = id; return s }

// SetMaxNodes overrides the node ceiling (<= 0 restores the default).
func (s *Spec) SetMaxNodes(n int) *Spec {
	if n <= 0 {
		n = DefaultMaxNodes
	}
	s.maxNodes = n
	return s
}

// Graph is a validated, frozen DAG ready to Execute. Its fields are unexported so
// a built graph is immutable — the topology cannot change after Build's
// validation.
type Graph struct {
	nodes    map[string]Node
	order    []string          // topological order (Kahn); order[len-1] is the terminal/sink
	inEdges  map[string][]Edge // edges keyed by destination node ID
	outEdges map[string][]Edge // edges keyed by source node ID
	entry    string
}

// Terminal returns the sink node ID: the last node in topological order, whose
// Output is surfaced as the graph's overall Output.
func (g *Graph) Terminal() string {
	if len(g.order) == 0 {
		return ""
	}
	return g.order[len(g.order)-1]
}

// Build validates and freezes the spec into a Graph. It rejects, up front:
//   - an empty graph,
//   - duplicate node IDs,
//   - an edge referencing an unknown node ID,
//   - a duplicate edge (same From->To),
//   - a self-loop (From == To),
//   - a node count over the ceiling,
//   - a declared entry that does not exist or is not a source (has in-edges),
//   - a CYCLE (Kahn topological sort leaves a residual set) — v1 is DAG-only
//     (ADR-0060 §Concurrency & topology): a cycle multiplies LLM spend unboundedly
//     against the quota gate, and the single-agent critic loop already covers the
//     common cyclic case.
//
// The node.Tools ⊆ Permitted(node.Scopes) gate is NOT checked here — it needs the
// live tool Registry, which lives in the runtime, so it is enforced at Execute
// time before any node runs (still "up front" relative to execution). See
// Execute.
func Build(s *Spec) (*Graph, error) {
	if len(s.nodes) == 0 {
		return nil, fmt.Errorf("graph: no nodes")
	}
	maxNodes := s.maxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}
	if len(s.nodes) > maxNodes {
		return nil, fmt.Errorf("graph: %d nodes exceed the ceiling of %d", len(s.nodes), maxNodes)
	}

	nodes := make(map[string]Node, len(s.nodes))
	orderIn := make([]string, 0, len(s.nodes)) // node IDs in insertion order (stable scheduling)
	for _, n := range s.nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("graph: a node has an empty ID")
		}
		if _, dup := nodes[n.ID]; dup {
			return nil, fmt.Errorf("graph: duplicate node ID %q", n.ID)
		}
		nodes[n.ID] = n
		orderIn = append(orderIn, n.ID)
	}

	inEdges := make(map[string][]Edge, len(s.nodes))
	outEdges := make(map[string][]Edge, len(s.nodes))
	seen := make(map[string]struct{}, len(s.edges))
	for _, e := range s.edges {
		if _, ok := nodes[e.From]; !ok {
			return nil, fmt.Errorf("graph: edge from unknown node %q", e.From)
		}
		if _, ok := nodes[e.To]; !ok {
			return nil, fmt.Errorf("graph: edge to unknown node %q", e.To)
		}
		if e.From == e.To {
			return nil, fmt.Errorf("graph: self-loop on node %q (cycles are rejected)", e.From)
		}
		key := e.From + "\x00" + e.To
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("graph: duplicate edge %q -> %q", e.From, e.To)
		}
		seen[key] = struct{}{}
		outEdges[e.From] = append(outEdges[e.From], e)
		inEdges[e.To] = append(inEdges[e.To], e)
	}

	// Cycle detection runs BEFORE the entry-source check so a cyclic graph is
	// reported as a cycle (the fundamental error), not incidentally as "entry has
	// an in-edge".
	order, err := kahnTopoSort(orderIn, inEdges, outEdges)
	if err != nil {
		return nil, err
	}

	if s.entry != "" {
		if _, ok := nodes[s.entry]; !ok {
			return nil, fmt.Errorf("graph: entry %q is not a declared node", s.entry)
		}
		if len(inEdges[s.entry]) != 0 {
			return nil, fmt.Errorf("graph: entry %q is not a source (it has %d in-edge(s))", s.entry, len(inEdges[s.entry]))
		}
	}

	return &Graph{
		nodes:    nodes,
		order:    order,
		inEdges:  inEdges,
		outEdges: outEdges,
		entry:    s.entry,
	}, nil
}

// kahnTopoSort returns a topological order of the node IDs, or an error naming a
// node still on a cycle. It is stable: the ready set is drained in insertion
// order, so a given spec always produces the same order (and thus the same
// terminal/sink) — important for a deterministic GraphResult.Output.
func kahnTopoSort(orderIn []string, inEdges, outEdges map[string][]Edge) ([]string, error) {
	indeg := make(map[string]int, len(orderIn))
	for _, id := range orderIn {
		indeg[id] = len(inEdges[id])
	}

	// Seed the queue with sources, in insertion order (stability).
	queue := make([]string, 0, len(orderIn))
	for _, id := range orderIn {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}

	order := make([]string, 0, len(orderIn))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		for _, e := range outEdges[n] {
			indeg[e.To]--
			if indeg[e.To] == 0 {
				queue = append(queue, e.To)
			}
		}
	}

	if len(order) != len(orderIn) {
		// The residual set (indeg > 0) is exactly the nodes on/after a cycle.
		var stuck []string
		for _, id := range orderIn {
			if indeg[id] > 0 {
				stuck = append(stuck, id)
			}
		}
		return nil, fmt.Errorf("graph: cycle detected involving nodes %v (v1 is DAG-only)", stuck)
	}
	return order, nil
}
