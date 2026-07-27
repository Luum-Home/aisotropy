// agnes_client.go — provider-client selection for the adapter: the aisotropy
// façade's own OpenAI-compatible client, pointed at agnes-2.0-flash, injected
// DIRECTLY via aisotropy.WithLLM. No decorator.
//
// HISTORY (why there is so little here): this file used to hold a hand-rolled
// usage-capturing decorator, in two generations of bugs:
//
//  1. A bespoke SSE client implementing ONLY StreamChat — which silently
//     downgraded /chat/agent to the fragile agentloop JSON fallback because the
//     runtime discovers native tool-calling by a `client.(ToolCaller)` type
//     assertion (the Go interface-upgrade-loss trap; commit 48dbf62).
//  2. A ToolCaller-preserving usage decorator over NewOpenAICompatLLM (commit
//     674c0e8) — which fixed the downgrade but still HID the client's
//     ReasoningToolCaller capability (the façade did not export the type, so
//     the wrap could not forward it).
//
// Both existed only because the façade exposed no way to observe provider token
// spend. Commit be945de (Element E) moved that job into the product:
// llm.WithUsageMeter wraps the BASE client inside the runtime
// (aisotropy/app.go, one line above where the working-memory summarizer takes
// it), so EVERY provider call — main turns, reflection retries, summarizer
// condensations, CallTools — is metered, and App.Usage() returns the cumulative
// total (exactly what the deleted decorator accumulated). With the product
// metering usage, the honest adapter is the one a third-party embedder would
// actually write: hand WithLLM the raw façade client and read App.Usage()
// (see setUsage in app.go).
//
// Deleting the decorator is also a strict CAPABILITY UPGRADE: the raw
// NewOpenAICompatLLM client implements ToolCaller AND ReasoningToolCaller
// natively (aisotropy/aisotropy.go's constructor contract), and with no wrap
// in between neither capability can be lost. The now-exported
// aisotropy.ReasoningToolCaller lets agnes_client_test.go pin both assertions.
//
// stdlib only beyond the façade: zero new dependencies, MIT-clean.
package main

import (
	"fmt"
	"os"
	"strings"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// newProviderClient resolves the provider client from OPENAI_* env and returns the
// façade client DIRECTLY (no decorator — see the file doc comment). The key is
// read from the environment and never logged or embedded in error strings.
//
// ARENA_STUB=1 (stubMode, app.go) short-circuits this entirely: it clears the
// real-backend-tier env vars this process may have inherited from run.sh
// (clearBackendTierEnv) and returns (nil, nil) instead of erroring on a
// missing OPENAI_API_KEY. Every mode's call site already does
// `agnes, err := newProviderClient(); if err != nil { return err }` followed by
// `newApp(agnes, ...)` / `setUsage(out, ...)` — newApp is nil-safe (app.go) —
// so a nil, nil-error return here is a normal, successful "run offline" signal
// that needs no change at any call site: newApp then omits WithLLM entirely,
// so aisotropy.New() falls back to its own env-driven llm.FromEnv(), which
// (with every provider key unset) resolves to aisotropy's NATIVE keyless stub.
//
// Signature shape and stub behavior are IDENTICAL to the decorator-era version
// (env selection, nil-return, error text) — only the return type widens from
// the deleted *agnesClient to the façade interface the client always was.
// max_tokens (AISOTROPY_MAX_TOKENS, default 1024) and stream usage options are
// owned by the façade client's own request shaping, unchanged.
func newProviderClient() (aisotropy.LLMClient, error) {
	if stubMode() {
		clearBackendTierEnv()
		return nil, nil
	}
	// Integration lane: mock the MODEL, keep the TIERS real.
	//
	// Only the provider credentials are cleared, never backendTierEnvVars — that
	// is the whole distinction. With the keys gone, aisotropy.New() resolves the
	// model through llm.RegistryFromEnv(), which auto-selects the keyless native
	// StubClient; with the tier vars intact, the e5 embed server, Qdrant and
	// rqlite are the REAL ones. Clearing the keys is not optional: run.sh sources
	// the repo-root .env, so a surviving key would make a lane that promised zero
	// model calls quietly call the provider — the exact leak the runner's guard
	// watches for.
	if stubModelMode() {
		for _, v := range providerEnvVars {
			_ = os.Unsetenv(v)
		}
		return nil, nil
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	// NO VENDOR DEFAULT. This used to fall back to a specific model id, which is
	// convenient locally and wrong in a public repo: anyone running the arena
	// without configuring it silently sent prompts to one company's endpoint,
	// and a board could be measured against a model nobody chose. Both supported
	// providers (and any other OpenAI-compatible endpoint) are configured the
	// same way — see docs/architecture/model-provider.md.
	base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		return nil, fmt.Errorf("OPENAI_MODEL is unset: set it (e.g. poolside/laguna-s-2.1 or agnes-2.0-flash) — this adapter refuses to pick a vendor for you; see docs/architecture/model-provider.md")
	}

	client := aisotropy.NewOpenAICompatLLM(key, model, base, "openai-compat")
	if _, ok := client.(aisotropy.ToolCaller); !ok {
		// The façade constructor documents that its client implements ToolCaller;
		// this can only fail if that contract regresses. Fail loudly rather than
		// inject a client that would silently downgrade /chat/agent to the
		// agentloop JSON fallback — the exact failure mode commit 48dbf62
		// diagnosed. (agnes_client_test.go pins this plus ReasoningToolCaller.)
		return nil, fmt.Errorf("agnes: NewOpenAICompatLLM client does not implement aisotropy.ToolCaller — /chat/agent would downgrade to the agentloop JSON fallback")
	}
	return client, nil
}
