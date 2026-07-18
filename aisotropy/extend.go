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
// Why these six "policy" seams behave differently from the memory backends: the
// policy seams are pure decision/transform logic with no authority over which
// tenant's data is touched, so they are aliased as raw, freely-implementable
// interfaces. The isolation-critical persistence backends (facts / semantic)
// are DELIBERATELY NOT aliased as raw stores: exposing a facts.Store /
// semantic.Store interface directly would let a consumer bypass the "every scope
// is built by the runtime from ctx" invariant (ADR-0011) that makes tenant
// isolation hold by construction. Those instead get the SEALED-DRIVER treatment
// (Tier-2 open-core): the consumer implements a NARROW, tenant-UNAWARE driver
// (VectorBackend / DocBackend — opaque namespace + opaque ids, no tenant
// concept), and the runtime wraps it in a sealed scoping decorator that derives
// the (tenant,user) namespace from the validated Scope on every call. So a
// community backend supplies persistence but never sees, chooses, or can forge a
// tenant — "bring your own backend, NOT your isolation". Do NOT move a
// facts.Store / semantic.Store into this file to "make it pluggable"; the driver
// seam is the isolation-safe path. The working-memory store, the tool Registry,
// and the memory Scope constructors remain runtime-owned with no consumer seam.
//
// Wiring status (honesty gate): the six policy aliases below are joined by the
// functional options that install a consumer's implementation into the runtime
// (WithGuardrailCheck / WithSummarizer / WithEvalJudge / WithModelPolicy /
// WithTenantRoutingPolicy / WithReranker), the sealed-driver options
// (WithVectorBackend / WithFactsBackend), and the plugin loaders (WithPlugin /
// WithPluginDir) — all defined at the bottom of this file and wired in app.go's
// startRuntime (WithVectorBackend threads through the shared ensureSemantic build
// path so the SDK ingestion surface and the chat/HTTP surface always share one
// sealed index). ALL SIX policy seams are now wired end-to-end (a supplied
// implementation is actually invoked by the running pipeline): the former
// stored-but-not-wired exception, Reranker, is APPLIED — the semantic hydration
// path (internal/api's hydrateSystemPrompt -> searchSemanticContext) consults a
// configured Reranker on the streaming /chat/stream and stateless /ask routes and
// in the bitrate "with memory" measurement, over an isolation-clean over-fetched
// candidate pool (ADR-0033). Every policy seam is ALSO fully
// third-party-implementable from an EXTERNAL module: the supporting payload
// types each method signature references are now re-exported here alongside the
// interface alias — GuardrailCheck (GuardrailCheckInput / GuardrailVerdict),
// Reranker (ScoredPoint / Point), Summarizer (Message), EvalJudge (Criterion /
// EvalSample / Score / Scale / UnitScale), ModelPolicy (Request / Decision), and
// TenantRoutingPolicy (TenantTier / TierShared / TierDedicated) — so a consumer
// implements any of the six against the public façade alone, with no internal
// import. The two SEALED-DRIVER (Tier-2) backends below are now on the same
// footing: VectorBackend's BackendVector/BackendHit and DocBackend's
// BackendDoc/BackendDocHit driver payload types are re-exported here alongside
// each backend interface alias, so an EXTERNAL module can fully implement
// WithVectorBackend / WithFactsBackend against the public façade alone — "bring
// your own vector/facts store" is complete. Inside this module (and this repo's
// tests) every seam is implementable.
package aisotropy

import (
	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/facts"
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
// The Check payload and the returned verdict are re-exported below
// (GuardrailCheckInput / GuardrailVerdict), so GuardrailCheck is fully
// implementable from an external module — no internal import required.
// Implementations MUST be safe under a cancellable context, MUST NOT panic, and
// should fail open (return a non-Suspicious verdict) on their own internal error,
// per the integrator's policy.
//
// Note: internal/guardrails intentionally exposes NO input-side check
// interface — only this output seam exists — so there is no InputCheck alias to
// add. Input-side defence (prompt-injection / redaction) is built in and not a
// consumer seam.
type GuardrailCheck = guardrails.OutputCheck

// GuardrailCheckInput is the payload a GuardrailCheck.Check receives: the
// original user Input, the model's Output under inspection, and the SystemPrompt
// in force (any field may be empty). Re-exported so an external module can
// implement GuardrailCheck against the public façade alone.
type GuardrailCheckInput = guardrails.OutputCheckInput

// GuardrailVerdict is what a GuardrailCheck.Check returns: Suspicious (true =
// flag/refuse the reply), the Signals that fired, and a Score. Its fields are
// public, so an external check constructs one as a plain struct literal.
type GuardrailVerdict = guardrails.Verdict

// Reranker reorders and trims the ANN candidates hydrated for a request before
// they enter the model's context, so only the few most relevant fragments are
// injected (ADR-0006/ADR-0033). Implement it to drop in a cross-encoder or a
// provider rerank API in place of the built-in score ordering. Alias for the
// real internal/semantic type the semantic hydration path consults: when a
// Reranker is registered (WithReranker), the retrieval path OVER-FETCHES a larger
// isolation-clean candidate pool and hands it to Rerank, injecting only the
// reordered top-K. The reranker runs strictly AFTER the per-scope isolation
// filter, so it can only reorder points the caller is already entitled to — it
// can never widen scope.
//
// Fixed shape:
//
//	Rerank(ctx context.Context, query string, candidates []ScoredPoint, topK int) []ScoredPoint
//
// The candidate/return element type (ScoredPoint) and its embedded Point are
// re-exported below, so Reranker is fully implementable from an external module —
// no internal import required. Rerank MUST NOT return more than topK.
type Reranker = semantic.Reranker

// ScoredPoint is one retrieval candidate handed to a Reranker: an embedded Point
// (the chunk's id/tenant/user/content/vector/…) annotated with its cosine Score
// to the query (higher = closer). Re-exported so an external Reranker can name
// the candidate/return element type — and, via the embedded Point below,
// construct candidates in its own tests. A reranker reorders and trims the
// candidates it is handed; it never needs to widen the pool.
type ScoredPoint = semantic.ScoredPoint

// Point is the embedded value inside a ScoredPoint: one retrieved knowledge chunk
// (ID, TenantID, UserID, Content, Source, Citation, Vector, CreatedAt). Its
// content-bearing fields are public, so a Reranker reads them directly (promoted
// through ScoredPoint) and constructs candidates as plain struct literals in its
// tests. The Kind field's type stays runtime-owned and is not needed to rerank
// (reordering never depends on kind), so it is left at its zero value externally.
type Point = semantic.Point

// Summarizer condenses messages evicted from the active working-memory buffer
// into an updated rolling "session state" paragraph (the summary buffer;
// docs/architecture/memory-tiers.md). Implement it to run condensation against
// a real LLM in place of the deterministic built-in. Alias for the real
// internal/memory type WorkingMemory consumes.
//
// Fixed shape:
//
//	Condense(ctx context.Context, existingSummary string, oldest []Message) (string, error)
//
// The evicted-message element type (Message) is re-exported below, so Summarizer
// is fully implementable from an external module — no internal import required.
type Summarizer = memory.Summarizer

// Message is one chat turn from the working-memory active buffer — the element
// type of the `oldest` slice a Summarizer.Condense receives. Its Role, Content,
// and Timestamp fields are public, so an external Summarizer reads them and
// builds Messages as plain struct literals in its tests. Re-exported so a
// Summarizer is implementable against the public façade alone.
type Message = memory.Message

// EvalJudge scores one sample against one criterion. It is the seam that
// unifies the three evaluation methods (an LLM judge, a deterministic code
// judge, and a labeled human gold set), each bound to exactly one criterion.
// Implement it to plug a custom scorer into a suite run. Alias for the real
// internal/eval type.
//
// Fixed shape:
//
//	Criterion() eval.Criterion
//	Score(ctx context.Context, sample EvalSample) (Score, error)
//
// The Criterion / EvalSample / Score payload types are re-exported below (along
// with Scale / UnitScale, which Criterion's rubric range needs), so EvalJudge is
// fully implementable from an external module — no internal import required.
// Score returns an error only for an OPERATIONAL failure (e.g. the model was
// unreachable); a legitimately low-quality sample is a low Score value, never
// an error, so a suite stays fail-closed on infrastructure but never on
// content.
type EvalJudge = eval.Judge

// Criterion is the one named quality axis an EvalJudge is bound to (returned by
// Criterion()): a stable Name (the aggregate key), a Description (the instruction
// shown to an LLM judge), and a Scale (the rubric range). All three fields are
// public, so an external judge returns its Criterion as a plain struct literal —
// e.g. Criterion{Name: "faithfulness", Description: "...", Scale: UnitScale}.
type Criterion = eval.Criterion

// EvalSample is the unit an EvalJudge.Score receives: the user Query, the Context
// assembled for the model (the grounding a faithfulness judge checks against),
// and the model's Response. All fields are public strings, so an external judge
// reads them directly and constructs samples in its tests.
type EvalSample = eval.EvalSample

// Score is what an EvalJudge.Score returns: the Criterion name, a Value ALWAYS
// normalized to [0,1] (a judge on a wider Scale normalizes before returning), and
// a short human-readable Reasoning. All fields are public, so an external judge
// builds one as a plain struct literal.
type Score = eval.Score

// Scale is the closed numeric interval a Criterion is scored on (Min, Max). It is
// only the human-facing rubric range — every Score.Value is normalized to [0,1]
// regardless — but it is re-exported because Criterion carries a Scale field, so
// an external judge can set a meaningful range instead of the zero value. Use
// UnitScale for the default 0..1.
type Scale = eval.Scale

// UnitScale is the default 0..1 Scale the built-in criteria use — re-exported so
// an external judge can assign the common rubric range to its Criterion without
// hand-constructing a Scale.
var UnitScale = eval.UnitScale

// ModelPolicy decides which registered LLM target (by registry name) a request
// should route to — the model-gateway routing seam (cost-aware downgrade,
// premium/cheap tiers, UI-selected model, ...). Implementations MUST be pure
// and deterministic: no I/O, no wall-clock, no randomness — that is what lets
// the routing layer be table-tested. Alias for the real internal/gateway type.
//
// Fixed shape:
//
//	Select(ctx context.Context, req Request) Decision
//
// The Request / Decision payload types are re-exported below, so ModelPolicy is
// fully implementable from an external module — no internal import required. A
// Decision with an empty Target means "no opinion" (the gateway resolves it to
// its default / the next policy in a chain).
type ModelPolicy = gateway.Policy

// Request is the routing-relevant projection of a chat request a ModelPolicy.Select
// inspects: the Prompt, a PromptTokensEstimate, a HasImage flag, the Tenant/User,
// an optional UISelector (UI-chosen target name), and CostSpentUSD. All fields are
// public, and it is a plain value type (no I/O, no client handle) — which is what
// keeps Select deterministic and table-testable. Re-exported so a policy reads its
// signals and builds Requests in its tests against the public façade alone.
type Request = gateway.Request

// Decision is what a ModelPolicy.Select returns: the Target (the registry NAME of
// the chosen LLM client, empty = "no opinion" → fall through to the next policy /
// gateway default) and a human-readable Reason for logs/audit. Both fields are
// public, so an external policy returns one as a plain struct literal.
type Decision = gateway.Decision

// TenantRoutingPolicy decides which tenancy tier a tenant is assigned to
// (Shared vs Dedicated), the ADR-0007 promotion seam: today an
// operator-configured static allowlist, tomorrow a dynamic control-plane source
// — without the Router or its callers changing. Implement it to answer tier
// assignment from a live source. Alias for the real internal/routing type.
//
// Fixed shape:
//
//	Tier(tenant string) TenantTier
//
// The TenantTier return type IS re-exported below, together with its two values
// as TierShared / TierDedicated constants, so TenantRoutingPolicy is fully
// implementable from an external module — no internal import required. An empty
// tenant string has no identity and MUST resolve to TierShared (the safe
// default), never TierDedicated.
type TenantRoutingPolicy = routing.Policy

// TenantTier is the physical routing tier a TenantRoutingPolicy.Tier assigns a
// tenant to (ADR-0007): the pooled/logically-isolated default or the physically
// separated tier. Re-exported so an external policy can name its return type; the
// two values are TierShared and TierDedicated below (an external module returns
// those constants — the underlying integer values stay runtime-owned).
type TenantTier = routing.TenantTier

// TierShared and TierDedicated are the two TenantTier values a TenantRoutingPolicy
// returns. TierShared is the safe default: the tenant's data lives in a pooled
// resource isolated by a tenant_id filter. TierDedicated is reserved for large or
// compliance-critical tenants that get a physically separate resource. They are
// re-exported so an external policy can return a tier without an internal import;
// an empty tenant MUST resolve to TierShared.
const (
	TierShared    = routing.Shared
	TierDedicated = routing.Dedicated
)

// VectorBackend is the SEALED-DRIVER seam for semantic (vector) persistence: the
// Tier-2 open-core path that lets a third party bring their OWN vector store
// (pgvector, Milvus, a managed ANN service, an in-memory ref) WITHOUT being
// handed the authority to bypass tenant isolation. It is a NARROW, deliberately
// tenant-UNAWARE driver — it addresses opaque namespaces and opaque ids/payloads
// and has NO tenant concept in its surface. Alias for the real internal/semantic
// interface.
//
// Fixed shape (see internal/semantic/backend.go for the full contract):
//
//	Upsert(ctx, ns string, items []BackendVector) error
//	Search(ctx, ns string, query []float32, k int) ([]BackendHit, error)
//	Delete(ctx, ns string, ids []string) error
//	Scan(ctx, ns string) ([]BackendVector, error)
//	Close() error
//
// Register it with WithVectorBackend: the runtime wraps it in a sealed scoping
// decorator (semantic.NewVectorBackendStore) that derives the (tenant,user)
// namespace from the caller's validated Scope on every call, so the backend only
// ever sees the hash for the exact tenant the runtime authorized at the Huma
// boundary — it can neither observe nor choose a tenant. The payload/return
// element types (BackendVector / BackendHit) are re-exported below, so a
// VectorBackend is now fully implementable from an EXTERNAL module — no internal
// import required. Author a conformance + isolation-proof test for an
// implementation with internal/plugintest's RunVectorBackendSuite.
type VectorBackend = semantic.VectorBackend

// BackendVector is one opaque stored item a VectorBackend receives at Upsert and
// returns from Scan: an ID (the decorator-supplied idempotency key), its Vector
// (the embedding, its length equal to the store dimension), and an opaque
// string->string Payload the backend persists verbatim without interpreting.
// Identity (tenant/user) is deliberately NOT in the payload — it lives only in
// the opaque namespace the sealed decorator derives — so a backend never sees or
// stores a tenant. Re-exported so an external VectorBackend can name its
// argument/return element type and build items as plain struct literals in its
// tests, against the public façade alone.
type BackendVector = semantic.BackendVector

// BackendHit is a BackendVector annotated with its similarity Score (higher =
// closer), the element VectorBackend.Search returns. Its embedded BackendVector's
// fields are promoted, so an external backend reads them directly and constructs
// hits as plain struct literals. Re-exported alongside BackendVector so
// VectorBackend.Search is expressible from an external module.
type BackendHit = semantic.BackendHit

// DocBackend is the SEALED-DRIVER seam for facts (document/task journal)
// persistence — the DocBackend analogue of VectorBackend. Bring your own
// key/value+search document store; the driver is NARROW and tenant-UNAWARE
// (opaque namespace + opaque keys, no tenant concept). Alias for the real
// internal/facts interface.
//
// Fixed shape (see internal/facts/backend.go for the full contract):
//
//	Put(ctx, ns, key string, doc BackendDoc) error
//	Get(ctx, ns, key string) (BackendDoc, bool, error)
//	Search(ctx, ns, query string, k int) ([]BackendDocHit, error)
//	Delete(ctx, ns string, keys []string) error
//	Scan(ctx, ns string) ([]BackendDoc, error)
//	ExportAll(ctx) ([]BackendDoc, error)
//	Close() error
//
// Register it with WithFactsBackend: the runtime seals it with
// facts.NewDocBackendStore so every scoped call is confined to the caller's
// namespace and cross-tenant access is structurally impossible. ExportAll is the
// single audited CROSS-tenant path (the sleep cycle's export); the sealed store
// reconstructs identity from stored fields there, matching the existing
// exportAll contract. The BackendDoc / BackendDocHit payload types are
// re-exported below, so a DocBackend is now fully implementable from an EXTERNAL
// module — no internal import required. Prove an implementation with
// internal/plugintest's RunDocBackendSuite (it includes the tenant-isolation
// proof).
type DocBackend = facts.DocBackend

// BackendDoc is one opaque stored fact document a DocBackend round-trips: a Key
// (the decorator-supplied idempotency key), the keyword-searchable Content, and
// an opaque string->string Fields map the backend persists verbatim. The backend
// treats Content as searchable text and Fields as an opaque blob; identity lives
// in the namespace, NOT here, except that ExportAll — the single audited
// cross-tenant path — requires the identity Fields be round-tripped verbatim so
// the sealed store can reconstruct each row's (tenant,user). Re-exported so an
// external DocBackend can name its argument/return type and build documents as
// plain struct literals in its tests, against the public façade alone.
type BackendDoc = facts.BackendDoc

// BackendDocHit is a BackendDoc annotated with the backend's keyword relevance
// Score (higher = better), the element DocBackend.Search returns; the decorator
// folds this into its Relevance×Importance×Recency final ranking. Its embedded
// BackendDoc's fields are promoted, so an external backend reads them directly.
// Re-exported alongside BackendDoc so DocBackend.Search is expressible from an
// external module.
type BackendDocHit = facts.BackendDocHit

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
	// reranker is APPLIED: startRuntime threads it onto the runtime (rt.reranker)
	// and the semantic hydration path (RegisterStream, RegisterAsk, and the
	// bitrate hydrator) consults it over an isolation-clean over-fetched candidate
	// pool. nil => the built-in score ordering (unchanged).
	reranker Reranker
	// vectorBackend, when set, is sealed by ensureSemantic into a
	// tenant-isolating semantic.Store (semantic.NewVectorBackendStore) in place of
	// the default in-memory/Qdrant store — shared by the chat/HTTP surface and the
	// SDK ingestion surface (one index per App). nil => the env/default store.
	vectorBackend VectorBackend
	// factsBackend, when set, is sealed by startRuntime into a tenant-isolating
	// facts.Store (facts.NewDocBackendStore) in place of the default
	// SQLite+FTS5/rqlite store. nil => the env/default store.
	factsBackend DocBackend
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

// WithReranker registers a custom Reranker for the semantic retrieval path, and
// it is APPLIED. When set, the runtime threads it into the semantic hydration
// path: the streaming /chat/stream route, the stateless /ask route, and the
// bitrate "with memory" measurement OVER-FETCH a larger candidate pool per scoped
// search, merge the isolation-clean results, and hand that pool to Rerank,
// injecting only its reordered top-K (ADR-0033). Because the reranker runs
// strictly AFTER the per-scope isolation filter, it can only reorder points the
// caller is already entitled to — it can never widen scope. A later of two calls
// wins. Unset => the built-in score ordering (unchanged; a nil reranker fetches
// exactly the injection window and orders by score, byte-for-byte the prior
// behavior).
//
// Scope note (honesty gate): the non-streaming /chat route (and the in-process
// App.Turn that shares it) does NOT hydrate semantic RAG through a reranker — it
// uses the working-memory-centric ChatCore path, which has no semantic reranking
// stage — so a Reranker affects the streaming, /ask, and bitrate paths, not
// plain Turn. This mirrors where semantic RAG hydration actually lives.
func WithReranker(r Reranker) Option {
	return func(a *App) { a.ext.reranker = r }
}

// WithVectorBackend installs a consumer-supplied VectorBackend as the semantic
// (vector) store, sealed into a tenant-isolating semantic.Store via the Tier-2
// scoping decorator (semantic.NewVectorBackendStore) so the community backend
// never sees or chooses a tenant — isolation is enforced by the runtime, not the
// backend ("bring your own backend, NOT your isolation"). It replaces the default
// in-memory / env-configured Qdrant store for BOTH the chat/HTTP RAG surface and
// the SDK IngestKnowledge surface (they share the one sealed index the App's
// ensureSemantic builds). The store enforces the active Embedder's dimension, so
// a wrong-width vector is rejected exactly like the built-in stores. A later of
// two calls wins. Unset => the env/default semantic store (byte-for-byte
// unchanged).
//
// Note: physical dedicated-tenant tier routing (AISOTROPY_DEDICATED_TENANTS) is
// NOT layered over a WithVectorBackend store — the consumer's backend owns its
// own persistence, and per-tenant isolation is provided by the sealed namespace
// decorator rather than a physically separate store. The runtime logs this when
// both are configured.
func WithVectorBackend(b VectorBackend) Option {
	return func(a *App) { a.ext.vectorBackend = b }
}

// WithFactsBackend installs a consumer-supplied DocBackend as the facts
// (document/task journal) store, sealed into a tenant-isolating facts.Store via
// facts.NewDocBackendStore so cross-tenant access is structurally impossible —
// the same open-core seal as WithVectorBackend, for the facts tier. It replaces
// the default in-process SQLite+FTS5 / networked rqlite store. A later of two
// calls wins. Unset => the env/default facts store (byte-for-byte unchanged).
//
// Note: as with WithVectorBackend, physical dedicated-tenant tier routing is not
// layered over a WithFactsBackend store (the backend owns persistence; isolation
// comes from the sealed namespace decorator). The runtime logs this when both a
// backend and a dedicated-tenant allowlist are configured.
func WithFactsBackend(b DocBackend) Option {
	return func(a *App) { a.ext.factsBackend = b }
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
