// Package aisotropy: the "bring your own" extension points (Wave 1A).
//
// aisotropy.go / knowledge.go / auth.go each re-export the one internal
// interface a consumer most obviously needs (Tool, ToolExecutor, LLMClient,
// Embedder, Authenticator). This file completes the *open-core* extension
// surface: it aliases the remaining SAFE, non-isolation-critical core seams so
// a third party can supply their own implementation of a runtime behaviour
// (guardrail judging, semantic reranking, summary condensation, eval scoring,
// model/tenant routing) WITHOUT forking the repo — the same façade contract as
// the other aliases, so a consumer's value is the genuine internal type the
// runtime consumes, not a shadow copy.
//
// Why these six and not the memory backends: the seams here are "policy" seams
// — pure decision/transform logic with no authority over which tenant's data is
// touched. Isolation-critical backends (the facts / semantic / working-memory
// stores, the tool Registry, the memory Scope and its constructors) are
// DELIBERATELY NOT aliased here: exposing them as freely-implementable
// interfaces would let an embedder bypass the "every scope is built by the
// runtime from ctx" invariant (ADR-0011) that makes tenant isolation hold by
// construction. Those get the sealed-driver treatment — a constrained,
// runtime-mediated registration path rather than a raw interface — in a later
// change. Do not move a store interface into this file to "make it pluggable".
//
// Wiring status (honesty gate, Wave 2): the six aliases below are joined by the
// functional options that install a consumer's implementation into the runtime
// (WithGuardrailCheck / WithSummarizer / WithEvalJudge / WithModelPolicy /
// WithTenantRoutingPolicy / WithReranker) plus the plugin loaders (WithPlugin /
// WithPluginDir), all defined at the bottom of this file and wired in app.go's
// startRuntime. FIVE of the six seams are wired end-to-end (a supplied
// implementation is actually invoked by the running pipeline); the SIXTH,
// Reranker, is deliberately STORED-BUT-NOT-WIRED this wave — the semantic
// hydration path does not yet consult a Reranker, so WithReranker captures the
// implementation and the runtime logs that it is not yet applied rather than
// faking a wiring (see WithReranker and startRuntime). Separately, some of these
// seams are still not fully third-party-implementable from an EXTERNAL module:
// their method signatures reference internal-only supporting types (the
// OutputCheckInput/Verdict, ScoredPoint, Message, Criterion/EvalSample/Score,
// Request/Decision, TenantTier payloads) that are not re-exported here yet — a
// pending follow-up. Inside this module (and this repo's tests) all six are
// implementable today.
package aisotropy

import (
	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// GuardrailCheck is the output-guardrail extension seam: implement it to attach
// a custom judge (an LLM-backed toxicity/faithfulness/policy classifier, or any
// deterministic heuristic) that the runtime's OutputInspector runs alongside
// its built-in toxicity and leak checks. It is an alias for the real internal
// interface, so a consumer's check is the exact value the inspector aggregates.
//
// Fixed shape:
//
//	Name() string
//	Check(ctx context.Context, in GuardrailCheckInput) GuardrailVerdict
//
// The Check payload (guardrails.OutputCheckInput) and the returned
// guardrails.Verdict are not re-exported yet, so implementing GuardrailCheck
// from an external module still requires those payload aliases — a pending
// follow-up. Implementations MUST be safe under a cancellable context, MUST NOT
// panic, and should fail open (return a non-Suspicious Verdict) on their own
// internal error, per the integrator's policy.
//
// Note: internal/guardrails intentionally exposes NO input-side check
// interface — only this output seam exists — so there is no InputCheck alias to
// add. Input-side defence (prompt-injection / redaction) is built in and not a
// consumer seam.
type GuardrailCheck = guardrails.OutputCheck

// Reranker reorders and trims the ANN candidates hydrated for a request before
// they enter the model's context, so only the few most relevant fragments are
// injected (ADR-0006). Implement it to drop in a cross-encoder or a
// provider rerank API in place of the built-in ordering. Alias for the real
// internal/semantic type used by the semantic store's hydration path.
//
// Fixed shape:
//
//	Rerank(ctx context.Context, query string, candidates []semantic.ScoredPoint, topK int) []semantic.ScoredPoint
//
// The candidate/return element type (semantic.ScoredPoint) is not re-exported
// yet, so a full external implementation needs that alias too — a pending
// follow-up. Rerank MUST NOT return more than topK.
type Reranker = semantic.Reranker

// Summarizer condenses messages evicted from the active working-memory buffer
// into an updated rolling "session state" paragraph (the summary buffer;
// docs/architecture/memory-tiers.md). Implement it to run condensation against
// a real LLM in place of the deterministic built-in. Alias for the real
// internal/memory type WorkingMemory consumes.
//
// Fixed shape:
//
//	Condense(ctx context.Context, existingSummary string, oldest []memory.Message) (string, error)
//
// The evicted-message element type (memory.Message) is not re-exported yet, so
// a full external implementation needs that alias too — a pending follow-up.
type Summarizer = memory.Summarizer

// EvalJudge scores one sample against one criterion. It is the seam that
// unifies the three evaluation methods (an LLM judge, a deterministic code
// judge, and a labeled human gold set), each bound to exactly one criterion.
// Implement it to plug a custom scorer into a suite run. Alias for the real
// internal/eval type.
//
// Fixed shape:
//
//	Criterion() eval.Criterion
//	Score(ctx context.Context, sample eval.EvalSample) (eval.Score, error)
//
// The Criterion / EvalSample / Score payload types are not re-exported yet, so
// a full external implementation needs those aliases too — a pending follow-up.
// Score returns an error only for an OPERATIONAL failure (e.g. the model was
// unreachable); a legitimately low-quality sample is a low Score value, never
// an error, so a suite stays fail-closed on infrastructure but never on
// content.
type EvalJudge = eval.Judge

// ModelPolicy decides which registered LLM target (by registry name) a request
// should route to — the model-gateway routing seam (cost-aware downgrade,
// premium/cheap tiers, UI-selected model, ...). Implementations MUST be pure
// and deterministic: no I/O, no wall-clock, no randomness — that is what lets
// the routing layer be table-tested. Alias for the real internal/gateway type.
//
// Fixed shape:
//
//	Select(ctx context.Context, req gateway.Request) gateway.Decision
//
// The Request / Decision payload types are not re-exported yet, so a full
// external implementation needs those aliases too — a pending follow-up. A
// Decision with an empty Target means "no opinion" (the gateway resolves it to
// its default / the next policy in a chain).
type ModelPolicy = gateway.Policy

// TenantRoutingPolicy decides which tenancy tier a tenant is assigned to
// (Shared vs Dedicated), the ADR-0007 promotion seam: today an
// operator-configured static allowlist, tomorrow a dynamic control-plane source
// — without the Router or its callers changing. Implement it to answer tier
// assignment from a live source. Alias for the real internal/routing type.
//
// Fixed shape:
//
//	Tier(tenant string) routing.TenantTier
//
// The TenantTier return type is not re-exported yet, so a full external
// implementation needs that alias too — a pending follow-up. An empty tenant
// string has no identity and MUST resolve to the Shared (safe) default, never
// Dedicated.
type TenantRoutingPolicy = routing.Policy

// appExt groups the Wave-2 "bring your own" extension wiring an App carries: the
// consumer implementations registered via the WithX options below, plus the
// plugin sources (WithPlugin / WithPluginDir). It is a sub-struct of App
// (app.go's `ext appExt` field) so the extension surface stays co-located with
// the seam aliases it implements.
//
// Every field is INERT when unset — startRuntime checks each and falls back to
// the exact built-in behavior — so New() with none of these options boots a
// byte-for-byte-identical runtime to the pre-Wave-2 composition root. That is
// the honesty gate for this wave: an option changes behavior only when a
// consumer actually supplies an implementation.
type appExt struct {
	// guardrailChecks are appended to the output inspector's checks (alongside the
	// built-in toxicity/leak heuristics and any LLM judge). Accumulates across
	// WithGuardrailCheck calls.
	guardrailChecks []GuardrailCheck
	// reranker is STORED ONLY this wave: the semantic hydration path does not yet
	// consult a Reranker (see startRuntime's honest-scope note). WithReranker
	// captures it; retrieval-wiring is a named internal/api follow-up.
	reranker Reranker
	// summarizer overrides the built-in memory.LLMSummarizer used to condense the
	// working-memory summary buffer. nil => the built-in LLM summarizer.
	summarizer Summarizer
	// evalJudge, when set, is used as the guardrails output judge in place of the
	// env-gated built-in eval.NewLLMJudge — attaching the judge check regardless of
	// the AISOTROPY_GUARDRAILS_LLM_JUDGE toggle. nil => the built-in behavior.
	evalJudge EvalJudge
	// modelPolicies are chained FIRST in the model-selection gateway (before the
	// built-in override/image/cost rules). Accumulates across WithModelPolicy calls.
	modelPolicies []ModelPolicy
	// tenantRoutingPolicy replaces the static AISOTROPY_DEDICATED_TENANTS allowlist
	// as the Router's tier policy. nil => the static policy (the default).
	tenantRoutingPolicy TenantRoutingPolicy
	// pluginPaths are individual manifest files (WithPlugin). Accumulates.
	pluginPaths []string
	// pluginDirs are directories scanned for manifests (WithPluginDir); the
	// AISOTROPY_PLUGIN_DIR env directory is appended to these at boot. Accumulates.
	pluginDirs []string
}

// WithGuardrailCheck registers one or more custom output guardrail checks. Each
// is appended to the runtime's OutputInspector and runs on every model response
// alongside the built-in toxicity and leak heuristics (and any attached LLM
// judge): a check returning a Suspicious verdict flags the response, and — when
// AISOTROPY_GUARDRAILS_BLOCK_OUTPUT is enabled — causes the non-streaming chat
// route to substitute a safe refusal. It accumulates: call it once per check, or
// pass several at once. Unset => the built-in heuristics only (unchanged).
func WithGuardrailCheck(checks ...GuardrailCheck) Option {
	return func(a *App) { a.ext.guardrailChecks = append(a.ext.guardrailChecks, checks...) }
}

// WithReranker registers a custom Reranker for the semantic retrieval path.
//
// HONEST SCOPE: this wave STORES the reranker but does not yet apply it — the
// semantic hydration path (internal/api) does not consult a Reranker, and wiring
// one requires internal/api changes beyond this wave's app.go scope. The option
// exists so the contract is nameable and the implementation is captured on the
// App; the runtime logs a warning at boot that the reranker is stored-not-wired.
// Retrieval-wiring is a named follow-up. See startRuntime's reranker note.
func WithReranker(r Reranker) Option {
	return func(a *App) { a.ext.reranker = r }
}

// WithSummarizer overrides the working-memory summary condenser. By default the
// runtime condenses the summary buffer with the built-in memory.LLMSummarizer
// backed by the chat client; supplying a Summarizer here replaces it (e.g. a
// deterministic condenser, or one backed by a cheaper dedicated model). Unset =>
// the built-in LLM summarizer (unchanged).
func WithSummarizer(s Summarizer) Option {
	return func(a *App) { a.ext.summarizer = s }
}

// WithEvalJudge registers a custom judge as the guardrails output-safety judge,
// used in place of the built-in eval.NewLLMJudge. When set, the judge is attached
// to the output inspector regardless of the AISOTROPY_GUARDRAILS_LLM_JUDGE env
// toggle (that toggle only gates the BUILT-IN judge); its block/annotate posture
// still follows AISOTROPY_GUARDRAILS_BLOCK_OUTPUT. Unset => the env-gated built-in
// behavior (unchanged). A later of two calls wins.
func WithEvalJudge(j EvalJudge) Option {
	return func(a *App) { a.ext.evalJudge = j }
}

// WithModelPolicy registers one or more consumer model-routing policies. They are
// chained FIRST in the model-selection gateway — ahead of the built-in
// override/image/cost rules — so a policy that returns a concrete target wins,
// and one that abstains (empty Target) falls through to the built-ins unchanged.
// It accumulates: call it once per policy, or pass several at once. Unset => the
// built-in gateway chain (unchanged).
func WithModelPolicy(policies ...ModelPolicy) Option {
	return func(a *App) { a.ext.modelPolicies = append(a.ext.modelPolicies, policies...) }
}

// WithTenantRoutingPolicy replaces the static AISOTROPY_DEDICATED_TENANTS
// allowlist as the tenancy-tier Router's policy, so tier assignment can come from
// a live source (a control-plane table, a compliance flag) instead of a fixed
// list. Unset => the static allowlist policy (the default). Note: this governs
// tier DECISIONS; physical provisioning of a dedicated store is still keyed off
// the env allowlist (see startRuntime), and with no dedicated tenants the Router
// is never consulted.
func WithTenantRoutingPolicy(p TenantRoutingPolicy) Option {
	return func(a *App) { a.ext.tenantRoutingPolicy = p }
}

// WithPlugin registers a single plugin manifest file (an `aisotropy-plugin.yaml`)
// by path. At boot the runtime parses and validates it (license allowlist and all
// — see internal/plugin), loads its tools through the same MCP path WithMCPServer
// uses, and merges them scope-gated into the toolset. It accumulates: call once
// per manifest. A manifest that fails to parse/validate/load is logged and
// skipped, never downing the runtime. Unset => no plugins (unchanged).
func WithPlugin(path string) Option {
	return func(a *App) { a.ext.pluginPaths = append(a.ext.pluginPaths, path) }
}

// WithPluginDir registers a directory scanned (non-recursively) for plugin
// manifest files (`*plugin*.yaml` / `*plugin*.yml`) at boot; every valid manifest
// found is loaded like WithPlugin. The AISOTROPY_PLUGIN_DIR env var adds one more
// such directory by default. It accumulates: call once per directory. Unset (and
// AISOTROPY_PLUGIN_DIR empty) => no directory scan (unchanged).
func WithPluginDir(dir string) Option {
	return func(a *App) { a.ext.pluginDirs = append(a.ext.pluginDirs, dir) }
}
