package graph

import (
	"context"
	"strings"
	"sync"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// fakeClient is a scripted llm.Client + llm.ToolCaller for driving the graph
// executor without a provider. It records every StreamChat / CallTools invocation
// (keyed by the node's system prompt, which is unique per node) so a test can
// assert WHICH nodes actually issued an LLM call — the core evidence for the
// fail-closed blocker (a downstream node that must never run issues zero calls).
type fakeClient struct {
	mu        sync.Mutex
	seen      []invocation
	text      func(system, prompt string) string       // text-path (no-tool node) reply
	tools     map[string][]llm.ToolCallResult          // system prompt -> scripted CallTools turns
	turns     map[string]int                           // per-system CallTools turn counter
	observe   func(ctx context.Context, system string) // optional hook (isolation test reads ctx identity)
	streamErr map[string]error                         // system prompt -> a provider error StreamChat returns
}

type invocation struct {
	kind   string // "stream" | "tools"
	system string
	prompt string
}

func newFakeClient() *fakeClient {
	return &fakeClient{tools: map[string][]llm.ToolCallResult{}, turns: map[string]int{}}
}

func (f *fakeClient) Name() string { return "fake" }

func (f *fakeClient) record(kind, system, prompt string, ctx context.Context) {
	f.mu.Lock()
	f.seen = append(f.seen, invocation{kind: kind, system: system, prompt: prompt})
	obs := f.observe
	f.mu.Unlock()
	if obs != nil {
		obs(ctx, system)
	}
}

// countSystem returns how many LLM invocations (stream or tools) a node with the
// given system prompt made — 0 proves the node never ran.
func (f *fakeClient) countSystem(system string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.seen {
		if v.system == system {
			n++
		}
	}
	return n
}

func (f *fakeClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	f.record("stream", system, prompt, ctx)
	if f.streamErr != nil {
		if err := f.streamErr[system]; err != nil {
			return nil, err
		}
	}
	reply := ""
	if f.text != nil {
		reply = f.text(system, prompt)
	}
	out := make(chan llm.Token)
	go func() {
		defer close(out)
		for _, w := range strings.Fields(reply) {
			select {
			case <-ctx.Done():
				return
			case out <- llm.Token{Text: w + " "}:
			}
		}
		select {
		case <-ctx.Done():
		case out <- llm.Token{Done: true, Usage: &llm.Usage{}}:
		}
	}()
	return out, nil
}

func (f *fakeClient) CallTools(ctx context.Context, system, prompt string, _ []llm.ToolSpec) (llm.ToolCallResult, error) {
	f.record("tools", system, prompt, ctx)
	f.mu.Lock()
	turn := f.turns[system]
	f.turns[system]++
	script := f.tools[system]
	f.mu.Unlock()
	if turn < len(script) {
		return script[turn], nil
	}
	return llm.ToolCallResult{Message: "done"}, nil
}

var _ llm.ToolCaller = (*fakeClient)(nil)
