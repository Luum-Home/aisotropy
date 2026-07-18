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

// newApp builds the shared App: the usage-capturing agnes client injected via
// WithLLM (OPENAI_* selects the real model), the request's system prompt carried
// as the product persona, and any extra options a mode needs (e.g. an
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
func newApp(agnes *agnesClient, persona string, extra ...aisotropy.Option) *aisotropy.App {
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

// setUsage folds the agnes client's accumulated provider-reported usage into the
// result (or -1/-1 when the provider never reported it, per the arena contract,
// or when agnes is nil — the ARENA_STUB offline path, where there is no
// provider to report usage at all).
func setUsage(out *result, agnes *agnesClient) {
	if agnes == nil {
		out.InputTokens, out.OutputTokens = -1, -1
		return
	}
	out.InputTokens, out.OutputTokens = agnes.usage()
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

// clearBackendTierEnv unsets every backendTierEnvVars entry IN-PROCESS,
// before aisotropy.New()/App.Run() is ever called, so aisotropy's own
// env-driven resolvers (aisotropy/app.go's embedder/semantic/facts-backend
// factories) fall back to their bare-binary in-process defaults (in-process
// cosine semantic tier, in-process SQLite+FTS5 facts tier, in-memory
// BadgerDB working memory) regardless of what the calling shell (run.sh)
// already exported into this process's environment. Called once from
// newAgnesClient's stub-mode branch, so it runs before ANY mode builds its
// App.
func clearBackendTierEnv() {
	for _, v := range backendTierEnvVars {
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
