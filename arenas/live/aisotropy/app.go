// app.go builds the aisotropy façade App the modes share and reads the
// provider-reported token usage back out. The App auto-wires its REAL backend
// tiers from the environment (AISOTROPY_EMBED_URL -> e5 APIEmbedder, QDRANT_HOST
// -> real Qdrant semantic tier, RQLITE_URL -> real rqlite facts tier,
// AISOTROPY_DATA_DIR -> on-disk BadgerDB working memory) — exactly the seams a
// production consumer configures — so no mode here falls back to the in-process
// stub stores. run.sh / run-docker.sh export those vars; the adapter never
// hardcodes them.
package main

import (
	"os"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

const productID = "arena"

// newApp builds the shared App: the raw façade agnes client injected via
// WithLLM (OPENAI_* selects the real model; no adapter decorator — usage is
// metered by the product itself, see setUsage), the request's system prompt
// carried as the product persona, and any extra options a mode needs (e.g. an
// authenticator for the App.Run modes). The semantic embedder + stores are
// resolved by the runtime from the environment, so a real e5/Qdrant/rqlite/Badger
// stack is used whenever those vars are set.
//
// agnes may be nil — the ARENA_STUB=1 offline path (resolved by
// newAgnesClient, see agnes_client.go): when nil, no WithLLM option is added
// at all, so aisotropy.New's own resolver falls back to llm.FromEnv(), which
// (with every provider key unset, as clearBackendTierEnv's sibling
// stub-mode check guarantees for a stub run) resolves to aisotropy's NATIVE
// keyless internal/llm.StubClient — not a hand-rolled fake this adapter
// invents. Every mode's `agnes, err := newAgnesClient()` call site is
// unchanged; nil-safety here is the single choke point that makes the
// ARENA_STUB toggle apply uniformly to all of them.
func newApp(agnes aisotropy.LLMClient, persona string, extra ...aisotropy.Option) *aisotropy.App {
	var opts []aisotropy.Option
	if agnes != nil {
		opts = append(opts, aisotropy.WithLLM(agnes))
	}
	if persona != "" {
		opts = append(opts, aisotropy.WithPersona(productID, aisotropy.Persona{BasePrompt: persona}))
	}
	opts = append(opts, extra...)
	return aisotropy.New(opts...)
}

// setUsage folds the PRODUCT-metered provider usage into the result: since
// commit be945de the runtime wraps its base client with llm.WithUsageMeter
// (aisotropy/app.go, one line above where the working-memory summarizer takes
// the client), and App.Usage() returns the cumulative total across every
// provider call the App has made — main turns, reflection retries, summarizer
// condensations, CallTools. That is exactly the accumulator the adapter's
// deleted agnes_client decorator used to hand-roll, so this reads the façade
// instead of an adapter-side counter.
//
// Cumulative is correct here BECAUSE the App's lifetime is one adapter
// invocation: main.go reads exactly one request from stdin, dispatches exactly
// one mode, and exits (main.go), and every mode builds its App fresh inside
// that dispatch (one newApp / bootServer per invocation — mode_*.go,
// server_boot.go). No App outlives a work item, so cumulative == this
// invocation's spend and no snapshot/delta is needed.
//
// -1/-1 (the arena contract's "no provider usage — honest absence") is
// reported when:
//
//   - stubMode(): ARENA_STUB=1 has NO provider at all — but aisotropy's native
//     keyless StubClient (which answers instead) attaches a DETERMINISTIC FAKE
//     Usage to its terminal token (internal/llm/stub.go's stubUsageFor), and
//     that fake now traverses the product usage meter too, so App.Usage() in a
//     stub run returns non-zero numbers that would render as real-looking
//     token counts on a stub board. The arena-full-matrix law is explicit:
//     stub tokens are provider-reported or -1, never fabricated. The stub gate
//     here is therefore load-bearing, not defensive — pinned by
//     TestSetUsage_StubLanePinsMinusOne (usage_report_test.go).
//   - the meter observed nothing (zero Usage): the provider never reported a
//     usage block (e.g. the call failed before any terminal token). A real
//     provider never reports a genuine 0/0, so zero is read as absence — the
//     same mapping the deleted decorator's sawUsage flag implemented.
//   - app is nil (defensive; no mode currently calls setUsage without an App).
func setUsage(out *result, app *aisotropy.App) {
	if stubMode() || app == nil {
		out.InputTokens, out.OutputTokens = -1, -1
		return
	}
	u := app.Usage()
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		out.InputTokens, out.OutputTokens = -1, -1
		return
	}
	out.InputTokens, out.OutputTokens = u.InputTokens, u.OutputTokens
}

// stubMode reports whether ARENA_STUB=1 requests aisotropy's keyless NATIVE
// stub LLM client (internal/llm/stub.go, automatically selected by
// llm.FromEnv() whenever OPENROUTER_API_KEY/OPENAI_API_KEY/ANTHROPIC_API_KEY
// are all unset — internal/llm/registry.go's FromEnv precedence) plus the
// bare-binary in-process memory-tier defaults, instead of the real networked
// tiers run.sh wires by default (see clearBackendTierEnv). Read fresh on
// every call (not cached at package init) so tests that set/unset the env
// var within one process observe the change.
func stubMode() bool {
	return os.Getenv("ARENA_STUB") == "1"
}

// backendTierEnvVars are every env var run.sh (and the Docker runner) export
// to point aisotropy at its REAL networked backend tiers (e5 embed server,
// Qdrant, rqlite) plus the on-disk BadgerDB working-memory data dir —
// mirroring services-config.md's "memory-tier variables" table.
var backendTierEnvVars = []string{
	"AISOTROPY_EMBED_URL", "AISOTROPY_EMBED_MODEL", "AISOTROPY_EMBED_DIM",
	"QDRANT_HOST", "QDRANT_PORT", "QDRANT_COLLECTION", "QDRANT_REST_URL",
	"RQLITE_URL", "AISOTROPY_DATA_DIR",
}

// providerEnvVars are every LLM-provider credential/selector run.sh sources
// from the repo-root .env. In a STUB run these MUST be cleared too: newApp
// omits WithLLM when agnes is nil, so aisotropy.New() resolves the model via
// llm.RegistryFromEnv(), which auto-selects the REAL provider for ANY key it
// finds (OPENROUTER_API_KEY → OPENAI_API_KEY → ANTHROPIC_API_KEY, see
// internal/llm/registry.go). If the key survives, a "stub" run silently calls
// the real agnes model (observed: ~20s latency, `llm=openai:agnes-2.0-flash`
// in the log, and 502s + billable quota burn under rate limiting) instead of
// the keyless in-process StubClient. Clearing them is what actually forces
// FromEnv down to the stub — this, not the store vars, was the reason
// aisotropy's stub lane was never truly offline.
var providerEnvVars = []string{
	"OPENROUTER_API_KEY", "OPENROUTER_MODEL",
	"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
	"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
	"AISOTROPY_MODEL",
}

// clearBackendTierEnv unsets every backendTierEnvVars AND providerEnvVars entry
// IN-PROCESS, before aisotropy.New()/App.Run() is ever called, so aisotropy's
// own env-driven resolvers fall back to their bare-binary in-process defaults:
// the store factories (aisotropy/app.go embedder/semantic/facts) to in-process
// cosine + SQLite+FTS5 + in-memory BadgerDB, AND llm.RegistryFromEnv() to the
// keyless in-process StubClient — regardless of what the calling shell (run.sh,
// which sources .env) exported into this process. Called once from
// newAgnesClient's stub-mode branch, so it runs before ANY mode builds its App.
// The provider-key clear is the load-bearing half: without it a stub run calls
// the real model (see providerEnvVars).
func clearBackendTierEnv() {
	for _, v := range backendTierEnvVars {
		_ = os.Unsetenv(v)
	}
	for _, v := range providerEnvVars {
		_ = os.Unsetenv(v)
	}
}

// sessionID returns the request session_id or a fresh unique one.
func sessionID(req *request) string {
	if req.SessionID != "" {
		return req.SessionID
	}
	return "arena-" + time.Now().Format("20060102-150405.000000000")
}
