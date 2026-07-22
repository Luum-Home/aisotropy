// agnes_client.go — a usage-capturing decorator over the aisotropy façade's own
// OpenAI-compatible client, pointed at agnes-2.0-flash.
//
// WHY THIS EXISTS (and why it no longer hand-rolls a transport): the live-arena
// contract requires per-turn provider token usage, and the aisotropy façade's
// App.Turn ChatOutput deliberately does NOT surface it — the only public seam
// that lets an embedder observe usage is aisotropy.WithLLM(client). The earlier
// version of this file injected a bespoke SSE client implementing ONLY
// StreamChat; because /chat/agent discovers native tool-calling by a
// `client.(ToolCaller)` type assertion, that StreamChat-only client SILENTLY
// downgraded the real lane's agent route to the fragile agentloop JSON fallback
// (the Go interface-upgrade-loss trap — the single largest source of the tools
// dimension's run-to-run variance). See commit 48dbf62.
//
// THE FIX (product half already landed): aisotropy.NewOpenAICompatLLM builds a
// client that natively implements ToolCaller (and ReasoningToolCaller), and
// ToolCallResult.Usage now carries provider usage on the CallTools path. This
// file's job is the adapter half: wrap that client in a decorator that
// PRESERVES the ToolCaller capability through the wrap (mirroring the product's
// own middlewareToolCaller shape) while accumulating usage from BOTH the
// StreamChat terminal Token AND the CallTools ToolCallResult. Losing ToolCaller
// again is prevented by a compile-time assertion below plus a runtime
// type-assertion test (agnes_client_test.go).
//
// stdlib only beyond the façade: zero new dependencies, MIT-clean.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// compile-time proof the injected type satisfies BOTH the public façade's
// LLMClient AND the optional ToolCaller capability — the assertion that would
// have caught the original downgrade bug the day it was written. If a future
// refactor drops CallTools (or the embedded StreamChat/Name), the module stops
// compiling here rather than silently falling back to the agentloop JSON path.
var (
	_ aisotropy.LLMClient  = (*agnesClient)(nil)
	_ aisotropy.ToolCaller = (*agnesClient)(nil)
)

// agnesClient decorates the façade's OpenAI-compatible client (inner) with a
// per-turn usage accumulator. A single App.Turn may drive StreamChat more than
// once (the reflection loop) and an agent run makes several CallTools turns;
// summing every reported usage block is the honest per-turn spend.
//
// The decorator forwards StreamChat and CallTools to inner, so the native
// tool-calling capability is carried THROUGH the wrap rather than lost by it.
type agnesClient struct {
	inner  aisotropy.LLMClient  // the façade client (NewOpenAICompatLLM)
	caller aisotropy.ToolCaller // inner asserted to ToolCaller (always non-nil here)

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	sawUsage     bool
}

// newAgnesClient builds the decorator from OPENAI_* env. The key is read from
// the environment and never logged or embedded in error strings.
//
// ARENA_STUB=1 (stubMode, app.go) short-circuits this entirely: it clears the
// real-backend-tier env vars this process may have inherited from run.sh
// (clearBackendTierEnv) and returns (nil, nil) instead of erroring on a
// missing OPENAI_API_KEY. Every mode's call site already does
// `agnes, err := newAgnesClient(); if err != nil { return err }` followed by
// `newApp(agnes, ...)` / `setUsage(out, agnes)` — both nil-safe (app.go) — so
// a nil, nil-error return here is a normal, successful "run offline" signal
// that needs no change at any call site: newApp then omits WithLLM entirely,
// so aisotropy.New() falls back to its own env-driven llm.FromEnv(), which
// (with every provider key unset) resolves to aisotropy's NATIVE keyless stub.
//
// Signature and stub behavior are IDENTICAL to the prior version — only the
// transport underneath changes (façade client instead of a hand-rolled SSE
// parser). max_tokens (AISOTROPY_MAX_TOKENS, default 1024) and stream usage
// options are now owned by the façade client's own request shaping, which reads
// the same env var — so that override is preserved, not dropped.
func newAgnesClient() (*agnesClient, error) {
	if stubMode() {
		clearBackendTierEnv()
		return nil, nil
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	// baseURL "" lets the façade default to the OpenAI base URL; OPENAI_MODEL ""
	// lets it fall through to our explicit agnes default below.
	base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "agnes-2.0-flash"
	}

	inner := aisotropy.NewOpenAICompatLLM(key, model, base, "agnes")
	caller, ok := inner.(aisotropy.ToolCaller)
	if !ok {
		// The façade constructor documents that its client implements ToolCaller;
		// this can only fail if that contract regresses. Fail loudly rather than
		// inject a client that would silently downgrade /chat/agent — the exact
		// failure mode this whole rewrite exists to prevent.
		return nil, fmt.Errorf("agnes: NewOpenAICompatLLM client does not implement aisotropy.ToolCaller — /chat/agent would downgrade to the agentloop JSON fallback")
	}
	return &agnesClient{inner: inner, caller: caller}, nil
}

// Name delegates to the inner façade client, which composes name+":"+model —
// with name "agnes" that is exactly the "agnes:"+model string this adapter has
// always reported for observability.
func (c *agnesClient) Name() string { return c.inner.Name() }

// usage returns the accumulated provider-reported token counts. When the
// provider never reported usage, it returns (-1, -1) per the arena contract.
func (c *agnesClient) usage() (in, out int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sawUsage {
		return -1, -1
	}
	return c.inputTokens, c.outputTokens
}

func (c *agnesClient) addUsage(in, out int) {
	c.mu.Lock()
	c.inputTokens += in
	c.outputTokens += out
	c.sawUsage = true
	c.mu.Unlock()
}

// StreamChat delegates to the inner client and folds the provider's
// stream-final usage (carried on any Token.Usage, terminal or otherwise) into
// the accumulator as tokens pass through. Honors ctx cancellation on send.
func (c *agnesClient) StreamChat(ctx context.Context, system, prompt string) (<-chan aisotropy.Token, error) {
	in, err := c.inner.StreamChat(ctx, system, prompt)
	if err != nil {
		return nil, err
	}
	out := make(chan aisotropy.Token)
	go func() {
		defer close(out)
		for tok := range in {
			if tok.Usage != nil {
				c.addUsage(tok.Usage.InputTokens, tok.Usage.OutputTokens)
			}
			select {
			case out <- tok:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// CallTools delegates to the inner ToolCaller — this is the method whose
// presence keeps /chat/agent on the native function-calling path — and folds
// the tool-call turn's provider usage (ToolCallResult.Usage, non-nil by default
// on the non-streaming CallTools request) into the accumulator. Usage is
// recorded even when err is non-nil, in case a partial usage block came back.
func (c *agnesClient) CallTools(ctx context.Context, system, prompt string, tools []aisotropy.ToolSpec) (aisotropy.ToolCallResult, error) {
	res, err := c.caller.CallTools(ctx, system, prompt, tools)
	if res.Usage != nil {
		c.addUsage(res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	return res, err
}

// NOTE ON ReasoningToolCaller (ADR-0061): the façade client ALSO implements the
// optional ReasoningToolCaller (CallTools + stateless reasoning carry-forward).
// This decorator cannot forward it: ReasoningToolCaller and its ReasoningState
// parameter are NOT re-exported on the aisotropy façade, and this adapter is a
// separate module that cannot import internal/llm to name that type. Wrapping
// therefore hides ReasoningToolCaller, so the runtime's
// `client.(ReasoningToolCaller)` assertion falls back to plain CallTools (which
// this decorator DOES intercept for usage). The only behavioral effect is that
// reasoning is not carried across agent turns — a documented graceful
// degradation to "carry nothing", identical to any plain ToolCaller
// (internal/llm/reasoning.go). This is a strictly smaller loss than the
// agentloop JSON fallback the rewrite eliminates. Re-exporting
// ReasoningToolCaller/ReasoningState on the façade would let a future version
// forward it; that is a façade change outside this adapter's ownership.
