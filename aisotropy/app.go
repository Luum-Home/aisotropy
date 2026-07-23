package aisotropy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/fugue-labs/gollem/ext/mcp"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/audit"
	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/consolidation"
	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/engram"
	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/health"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/plugin"
	"github.com/Luum-Home/aisotropy/internal/primitives"
	"github.com/Luum-Home/aisotropy/internal/responsecache"
	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/sleepcycle"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// Runtime tuning constants, lifted verbatim from cmd/aisotropyd/main.go so the
// builder boots a byte-for-byte equivalent runtime. They are internal knobs, not
// consumer surface.
const (
	// shutdownTimeout bounds how long graceful shutdown waits for in-flight HTTP
	// requests (mainly open SSE streams) to finish before giving up.
	shutdownTimeout = 10 * time.Second
	// outboxPollInterval is how often the consolidation worker checks for pending
	// outbox events when the queue is empty.
	outboxPollInterval = 1 * time.Second
	// agentMaxSteps bounds the tool-use loop on the agent endpoint.
	agentMaxSteps = 3
	// primitiveFrictionThreshold is the accrued friction at which the sleep cycle
	// rewrites a user's primitive.
	primitiveFrictionThreshold = 3.0
	// sleepCycleInterval is how often the offline DataFusion consolidation runs
	// when a worker binary is configured (Phase 6).
	sleepCycleInterval = 1 * time.Hour
	// turnPruneInterval is how often KindTurn vectors are evicted past their
	// TTL/cap (ADR-0017 Phase 3). Runs always (pure Go), independent of the
	// DataFusion sleep cycle.
	turnPruneInterval = 1 * time.Hour
	// bitrateInterval is how often the offline bits-per-token-per-tenant
	// measurement runs (Bet 6) when a scorer binary is configured.
	bitrateInterval = 1 * time.Hour
	// defaultSemanticTurnTTL is the age-TTL fallback for KindTurn vector
	// eviction (ADR-0017 phase 3, AISOTROPY_SEMANTIC_TURN_TTL) when the env
	// var is unset or invalid: 7 days. Bounded, not unbounded — the entire
	// point of Phase 3 is that raw-turn vectors do not grow forever.
	defaultSemanticTurnTTL = 7 * 24 * time.Hour
	// defaultSemanticTurnMax is the per-(tenant,user) KindTurn count-cap
	// fallback (AISOTROPY_SEMANTIC_TURN_MAX) when the env var is unset or
	// invalid.
	defaultSemanticTurnMax = 500
	// defaultFactMinImportance is the engram.Store.MinImportance fallback
	// (AISOTROPY_FACT_MIN_IMPORTANCE) when the env var is unset or invalid:
	// 0.0, i.e. always dual-index (ADR-0017 phase 2's original behavior, no
	// change from before this became configurable).
	defaultFactMinImportance = 0.0
	// readinessProbeTimeout bounds each dependency probe on GET /health.
	readinessProbeTimeout = 2 * time.Second
	// healthProbeTenant is the reserved identity the readiness probes scope their
	// read-only checks to, so a probe never touches real tenant data.
	healthProbeTenant = "_health"
	// circuitFailureThreshold / circuitCooldown configure the LLM circuit breaker
	// (Phase 7): after this many consecutive start failures the breaker opens and
	// calls fail fast until the cooldown elapses.
	circuitFailureThreshold = 5
	circuitCooldown         = 30 * time.Second
	// guardrailsJudgeSafetyThreshold is the normalized safety floor the opt-in
	// LLM-as-a-judge output check (AISOTROPY_GUARDRAILS_LLM_JUDGE) grades
	// against: a model response whose safety score (eval.Safety, 1.0 = safe)
	// falls STRICTLY below this is flagged Suspicious (and refused when
	// BLOCK_OUTPUT is on). 0.5 = flag anything the judge scores as more unsafe
	// than safe.
	guardrailsJudgeSafetyThreshold = 0.5
)

// Default rate-limit values used when neither WithRateLimit nor the
// AISOTROPY_RATE_PER_SEC / AISOTROPY_BURST env vars supply an override.
const (
	defaultRatePerSec = 10
	defaultBurst      = 30
)

// App is a configured Aisotropy runtime, built with New and booted with Run. It
// is the builder's product: an immutable-after-New snapshot of the wiring the
// composition root used to hand-code, now driven by functional options.
type App struct {
	addr          string
	toolset       []tools.Tool
	executor      ToolExecutor
	typedHandlers map[string]api.TypedToolHandler
	mcpServers    []mcpServerSpec
	personas      map[string]string
	graphs        map[string]*Graph
	llmClient     llm.Client
	ratePerSec    float64
	burst         float64
	rateLimitSet  bool
	inPricePer1M  float64
	outPricePer1M float64

	// ext groups the Wave-2 "bring your own" extension wiring: the consumer
	// implementations registered via the WithGuardrailCheck / WithReranker /
	// WithSummarizer / WithEvalJudge / WithModelPolicy / WithTenantRoutingPolicy
	// options and the WithPlugin / WithPluginDir sources. Every field is
	// inert-safe: unset leaves startRuntime byte-for-byte identical to the
	// pre-Wave-2 boot. Its type and the options that populate it live in
	// extend.go, alongside the seam aliases they implement.
	ext appExt

	// authenticator backs WithAuthenticator: the single fail-closed gate the
	// auth middleware invokes in front of every /v1 route. nil means no
	// authenticator was registered — Run then installs a default-deny gate
	// (Option C) that 401s every /v1 request. Use WithAuthenticator(NoAuth())
	// to explicitly run without a credential check.
	authenticator Authenticator

	// auditSink backs WithAuditSink (aisotropy/audit.go): the access-audit sink
	// the /v1 audit middleware writes one Event to per served request (Gap 8).
	// nil means "unset" — Run builds the env-driven default via audit.FromEnv
	// (durable JSONL file when AISOTROPY_AUDIT_LOG is set, else a dedicated slog
	// "access_audit" logger), so there is always an access trail.
	auditSink audit.Sink

	// devUIPath backs WithDevUI / WithDevUIAt (aisotropy/devui.go): the
	// non-/v1 route the built-in development chat page mounts on. Empty (the
	// default) means the page is NOT registered — GET /ui 404s — so a default
	// build has no dev surface and no drift. The page is a development
	// convenience only (unauthenticated, single-page); see WithDevUI.
	devUIPath string

	// mcpServe backs WithMCPServe (aisotropy/mcpserve.go): the chi route the
	// scope-gated MCP *server* endpoint mounts on. nil (the default) means the
	// endpoint is NOT registered and the runtime stays MCP consume-only; when set,
	// Run mounts a stateless streamable-HTTP MCP server that publishes the caller's
	// scope-permitted tools. See WithMCPServe.
	mcpServe *mcpServeSpec

	// embedder / chunkSize / chunkOverlap back WithEmbedder / WithChunkSize
	// (ADR-0018, aisotropy/knowledge.go). chunkSize <= 0 means "unset": the
	// façade falls back to semantic.DefaultChunkOptions().
	embedder     semantic.Embedder
	chunkSize    int
	chunkOverlap int

	// extractor backs WithExtractor (aisotropy/ingest_file.go): the swappable
	// byte->text seam IngestFile runs in front of the IngestKnowledge pipeline.
	// nil means "unset" — IngestFile falls back to ingest.DefaultExtractor()
	// (PDF + HTML + plain text), mirroring the embedder's BYO-or-default idiom.
	extractor Extractor

	// semanticOnce/semanticStore/semanticErr memoize ensureSemantic's
	// lazily-built embedder+store pair, shared between Run and
	// IngestKnowledge (aisotropy/knowledge.go) so the two surfaces never
	// build two independently evolving semantic indices for the same App.
	semanticOnce  sync.Once
	semanticStore semantic.Store
	semanticErr   error

	// closeOnce/closeErr back Close (below): idempotent release of resources
	// this App memoized outside of Run's own lifecycle (the lazily built
	// semantic store, and — once Turn has been called — the lazily built
	// in-process runtime).
	closeOnce sync.Once
	closeErr  error

	// turnOnce/runtime/turnErr memoize the in-process runtime App.Turn builds
	// lazily on its first call (aisotropy/turn.go), mirroring the
	// semanticOnce idiom above: the memory tiers, model pipeline, tools,
	// guardrails, cost meter, rate limiter, and background workers are booted
	// exactly once and reused across turns (so working memory persists between
	// them), then torn down by Close. A Run()-served App builds its OWN runtime
	// local to Run instead, so the two entry points never share — nor
	// double-build — a runtime for the same App.
	turnOnce sync.Once
	runtime  *appRuntime
	turnErr  error

	// usageOnce/usageAcc back App.Usage() (aisotropy/usage.go): the App-scoped
	// provider-usage accumulator startRuntime composes into the base LLM
	// client(s). Lazily built via usageAccumulator() so App.Usage() before any
	// runtime boot returns zeros without triggering a build.
	usageOnce sync.Once
	usageAcc  *llm.UsageMeter

	// configFile backs WithConfigFile: an explicit runtime config-file path
	// (aisotropy/runtimeconfig.go). Empty means startRuntime falls back to the
	// AISOTROPY_CONFIG_FILE env var and then the default search paths. The file's
	// values are materialized into the process environment under env > file >
	// default precedence, once, guarded by configOnce.
	configFile string
	configOnce sync.Once
	configErr  error
}

// Option configures an App at construction time. Options are applied in order;
// a later option of the same kind overrides an earlier one (except WithTool,
// which accumulates).
type Option func(*App)

// New builds an App from the given options. Unset options fall back to the same
// env-driven defaults the standalone binary used, so New() with no options boots
// an equivalent runtime.
func New(opts ...Option) *App {
	a := &App{
		personas:   map[string]string{},
		graphs:     map[string]*Graph{},
		ratePerSec: defaultRatePerSec,
		burst:      defaultBurst,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// WithAddr sets the HTTP listen address. When unset, the address is resolved by
// [ResolveListenAddr]: AISOTROPY_ADDR, then $PORT (as ":"+PORT), then ":8080".
func WithAddr(addr string) Option {
	return func(a *App) { a.addr = addr }
}

// ResolveListenAddr resolves the HTTP listen address with precedence
// optAddr (WithAddr) > AISOTROPY_ADDR > $PORT (as ":"+PORT) > ":8080".
//
// $PORT support lets aisotropyd bind the port injected by container-serverless
// platforms (Cloud Run, Knative, Azure Container Apps, Fly, Heroku-style) with no
// extra config. The $PORT form is ":"+PORT — all interfaces, i.e. non-loopback — so
// the NoAuth non-loopback guard still applies and secure-by-default is preserved.
func ResolveListenAddr(optAddr string) string {
	if optAddr != "" {
		return optAddr
	}
	if a := os.Getenv("AISOTROPY_ADDR"); a != "" {
		return a
	}
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// WithConfigFile points the runtime at a config FILE (YAML or TOML) whose values
// mirror the AISOTROPY_* / service env vars (aisotropy/runtimeconfig.go). At boot
// the file is loaded and its values are materialized into the process
// environment under env > file > default precedence: an explicit env var always
// wins over the file, and any knob neither the env nor the file sets keeps its
// built-in default. Passing "" (or omitting this option) falls back to the
// AISOTROPY_CONFIG_FILE env var and then the default search paths, so a bare
// App with no config file behaves exactly as before. A malformed file makes the
// boot (Run/Turn) fail with a clear error rather than panic.
func WithConfigFile(path string) Option {
	return func(a *App) { a.configFile = path }
}

// WithTool registers a tool. It accumulates: call it once per tool. The runtime
// builds the scope-gated registry from the collected tools.
func WithTool(t Tool) Option {
	return func(a *App) { a.toolset = append(a.toolset, t) }
}

// WithToolExecutor sets the ToolExecutor that runs registered tools, replacing
// the demo executor. A single executor dispatches every tool by name.
func WithToolExecutor(exec ToolExecutor) Option {
	return func(a *App) { a.executor = exec }
}

// WithPersona registers a per-product base prompt. The runtime composes a
// provider over all registered personas, falling back to the built-in default
// persona for any product_id not registered here.
func WithPersona(productID string, p Persona) Option {
	return func(a *App) { a.personas[productID] = p.BasePrompt }
}

// WithLLM sets the LLM client. Default: llm.FromEnv() (real provider when
// ANTHROPIC_API_KEY is set, otherwise the credential-less stub).
func WithLLM(client LLMClient) Option {
	return func(a *App) { a.llmClient = client }
}

// WithRateLimit sets the per-tenant token-bucket rate (requests/sec) and burst.
// Default: 10 / 30 (overridable via AISOTROPY_RATE_PER_SEC / AISOTROPY_BURST
// only when WithRateLimit is not used).
func WithRateLimit(perSec, burst float64) Option {
	return func(a *App) {
		a.ratePerSec = perSec
		a.burst = burst
		a.rateLimitSet = true
	}
}

// WithCostPricing sets the USD prices per 1M input/output tokens used for the
// per-tenant /costs estimate. Default: the runtime's built-in price table.
func WithCostPricing(inPer1M, outPer1M float64) Option {
	return func(a *App) {
		a.inPricePer1M = inPer1M
		a.outPricePer1M = outPer1M
	}
}

// appRuntime is the live, reusable core an App boots once: the memory tiers,
// the model pipeline (circuit-broken + gateway), the scope-gated tools, the
// guardrails, the cost meter, the rate limiter, and the background
// consolidation/maintenance workers. Run() serves it over HTTP; Turn() calls
// it in-process (aisotropy/turn.go). It is built once per App and shared —
// there are never two independently evolving runtimes for the same App, exactly
// as ensureSemantic guarantees one semantic index per App.
//
// The fields are the union of what the two entry points need: the chat pipeline
// deps (client/wm/factsStore/limiter/toolRegistry/persona/costMeter/gateway/
// guard — the ChatDeps Turn assembles and Register/RegisterStream also read),
// the streaming/agent/ask extras Run wires on top (streamClient,
// cheapStreamClient, semanticStore, embedder, typedHandlers, executor,
// frictionDetector, tracerProvider, bitrateStore, bptSampler), and the
// lifecycle handles (close, guarded by closeOnce for idempotency).
type appRuntime struct {
	// Chat pipeline (the ChatDeps Turn builds; Register also consumes these).
	client        llm.Client // protectedClient: the circuit-broken chat client
	wm            *memory.WorkingMemory
	factsStore    facts.Store
	limiter       *memory.Limiter
	toolRegistry  *tools.Registry
	persona       api.PersonaProvider
	costMeter     *costmeter.Meter
	gateway       *gateway.Gateway
	guard         *api.Guard
	semanticStore semantic.Store
	embedder      semantic.Embedder
	reranker      semantic.Reranker // consumer WithReranker (nil => built-in score ordering)

	// HTTP-only extras Run layers on top of the shared deps above (Turn does
	// not touch these — the streaming/ask/agent/ops surfaces are HTTP-only).
	streamClient      llm.Client // OTel-decorated protectedClient (streaming + gateway primary)
	cheapStreamClient llm.Client // optional quota-pressure degrade client (nil when unconfigured)
	typedHandlers     map[string]api.TypedToolHandler
	executor          ToolExecutor
	frictionDetector  *primitives.FrictionDetector
	tracerProvider    trace.TracerProvider
	bitrateStore      *bitrate.Store
	bptSampler        bitrate.SampleRecorder

	// Lifecycle. closeFn performs the exact teardown Run's graceful shutdown
	// does (stop workers, close MCP clients, close every store, shut tracing);
	// close wraps it in closeOnce so it is idempotent (Run calls it once on
	// shutdown; App.Close calls it once for a Turn-built runtime).
	closeFn   func(context.Context)
	closeOnce sync.Once
}

// close tears the runtime down idempotently — workers stopped before the stores
// they write to are closed, exactly as Run's graceful shutdown sequences it. The
// ctx bounds the tracer's flush deadline (Run passes its bounded shutdown ctx).
func (rt *appRuntime) close(ctx context.Context) {
	rt.closeOnce.Do(func() { rt.closeFn(ctx) })
}

// waitWorkers blocks until every worker tracked by wg has returned, or until ctx
// is done — whichever comes first. It is how closeFn joins the background workers
// after cancelling their context but before closing the stores they touch, so a
// worker mid-operation cannot race a store Close. The wait is bounded so a stuck
// worker degrades teardown to best-effort (close anyway) rather than hanging it.
func waitWorkers(ctx context.Context, wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// clientSupportsToolCalling reports whether c can drive the provider's NATIVE
// tool-calling API (llm.ToolCaller) — the exact capability chat_agent.go's
// handler discovers by `client.(llm.ToolCaller)` to choose native function-calling
// over the agentloop JSON fallback. Extracted as a pure predicate so the boot-time
// warning decision is unit-testable without capturing slog output.
func clientSupportsToolCalling(c llm.Client) bool {
	_, ok := c.(llm.ToolCaller)
	return ok
}

// startRuntime builds the shared runtime core and starts its background workers,
// returning it with a close() that performs the full teardown. It is the single
// dependency-construction path both Run (HTTP) and Turn (in-process) boot from,
// so neither entry point forks a second env-reading wiring. The construction
// order — stores opened, workers started, clients decorated, tools discovered,
// guardrails/gateway assembled — is byte-for-byte the order Run used to inline,
// so the boot logs and side effects are unchanged; the only code that stays in
// Run is the HTTP surface (router, routes, authn middleware, server) built ON
// TOP of the returned runtime.
//
// ctx is used for construction-time work (cold-boot outbox reconciliation, the
// semantic store dial, MCP client startup). The background workers run under an
// independent context (workerCtx, derived from context.Background()) so they
// outlive ctx and keep draining until close() stops them — the same lifetime the
// inlined workers had. On a mid-construction store-open failure the
// already-opened stores are closed before returning the error, as Run did.
//
//nolint:gocognit,cyclop,funlen // large cohesive runtime constructor; refactor tracked in docs/architecture/phases-3-7-as-built.md debt ledger
func (a *App) startRuntime(ctx context.Context) (*appRuntime, error) {
	// Materialize the optional runtime config file into the process environment
	// BEFORE any env-reading resolver runs (llm.FromEnv reads ANTHROPIC_API_KEY
	// immediately below, and the whole constructor reads AISOTROPY_* from the
	// environment). env > file > default precedence is enforced inside
	// materialize (already-set env vars are left untouched); a no-config app
	// materializes nothing and boots identically. Guarded so it runs once per App.
	if err := a.applyConfigFile(); err != nil {
		return nil, err
	}

	llmClient := a.llmClient
	if llmClient == nil {
		llmClient = llm.FromEnv()
	}

	// Usage accumulation (Element E): wrap the BASE client with the App's
	// usage meter BEFORE anything consumes it. This is the one choke point
	// every provider call passes through — including the working-memory
	// summarizer immediately below, which receives THIS client (not the
	// middleware/breaker/OTel-composed one), so a later hook would undercount
	// summarizer condensations relative to the honest all-calls total.
	// WithUsageMeter preserves ToolCaller/ReasoningToolCaller, so the
	// capability checks and decorators downstream see the base client's full
	// surface unchanged. Read the total via App.Usage().
	llmClient = llm.WithUsageMeter(llmClient, a.usageAccumulator())

	// Working memory: on-disk BadgerDB when AISOTROPY_DATA_DIR is set, else
	// in-memory. The summarizer is the consumer's WithSummarizer when supplied,
	// else the built-in memory.LLMSummarizer backed by the chat LLM so the
	// condensed buffer is a real condensation.
	wm, err := newWorkingMemory(llmClient, a.ext.summarizer)
	if err != nil {
		return nil, err
	}

	// Physical multi-tenancy (ADR-0007): tenants in AISOTROPY_DEDICATED_TENANTS
	// get their own physical facts/semantic resource; everyone else shares the
	// pool. Empty (default) = no routing.
	dedicatedTenants := splitCSV(os.Getenv("AISOTROPY_DEDICATED_TENANTS"))
	// Tier policy: the consumer's WithTenantRoutingPolicy when supplied, else the
	// static allowlist from AISOTROPY_DEDICATED_TENANTS (the default). The policy
	// governs which tier the Router assigns a tenant. Note (honest scope): the
	// PHYSICAL provisioning of dedicated facts/semantic stores is still keyed off
	// the env allowlist below (openFactsStore/openSemanticStore receive
	// dedicatedTenants) — a dynamic policy changes tier DECISIONS, but a tenant it
	// promotes to Dedicated only gets a physically separate store when it is also
	// in the env allowlist. With no dedicated tenants the runtime uses a pooled
	// store directly and the Router (hence the policy) is never consulted.
	var tierPolicy routing.Policy = routing.NewStaticPolicy(dedicatedTenants...)
	if a.ext.tenantRoutingPolicy != nil {
		tierPolicy = a.ext.tenantRoutingPolicy
		slog.Info("custom tenant-routing policy installed (WithTenantRoutingPolicy); static allowlist bypassed for tier decisions")
	}
	tierRouter := routing.NewRouter(tierPolicy)
	if len(dedicatedTenants) > 0 {
		slog.Info("dedicated-tenant routing enabled", "tenants", dedicatedTenants)
	}

	// Facts memory (Phase 3): a consumer's WithFactsBackend sealed into a
	// tenant-isolating store (Tier-2 open-core) wins; else SQLite+FTS5 / rqlite by
	// env (ADR-0007 routing). The sealed backend enforces per-tenant isolation via
	// the namespace decorator, so physical dedicated-tenant tier routing is not
	// layered over it (the backend owns its own persistence) — logged when both a
	// backend and a dedicated allowlist are configured.
	var factsStore facts.Store
	if a.ext.factsBackend != nil {
		factsStore = facts.NewDocBackendStore(a.ext.factsBackend)
		if len(dedicatedTenants) > 0 {
			slog.Warn("facts tier routing not applied to a WithFactsBackend store (sealed decorator provides per-tenant isolation; the community backend owns its own persistence)", "dedicated", dedicatedTenants)
		}
		slog.Info("custom facts backend installed (WithFactsBackend); sealed into a tenant-isolating store")
	} else {
		factsStore, err = openFactsStore(tierRouter, dedicatedTenants) //nolint:contextcheck // facts.OpenSQLite has no context parameter (internal/facts, out of scope for this cleanup)
		if err != nil {
			_ = wm.Close()
			return nil, err
		}
	}
	factsExtractor := facts.HeuristicExtractor{}

	// Semantic memory (Phase 4): a BYO WithEmbedder, else an env-configured
	// APIEmbedder (AISOTROPY_EMBED_URL), else the deterministic HashEmbedder
	// placeholder (see knowledge.go's ensureSemantic doc for the full
	// precedence) + in-memory/Qdrant by env, built through ensureSemantic
	// (ADR-0018) so IngestKnowledge shares exactly this store/embedder pair
	// whether it was called before Run (and already built them) or is
	// called later, alongside the running server.
	semanticStore, embedder, err := a.ensureSemantic(ctx)
	if err != nil {
		_ = wm.Close()
		_ = factsStore.Close()
		return nil, err
	}

	// Bitrate (Bet 6): the durable, tenant-scoped bpt store, gated on the scorer
	// binary. When enabled it backs both the held-out sampler (the request path
	// reserves ~5% of turns into it, excluded from consolidation) and the offline
	// measurement coordinator. Disabled (bin unset) → nil store, nil sampler, no
	// /bitrate route: turn ingestion is byte-for-byte unchanged.
	var (
		bitrateStore *bitrate.Store
		bptSampler   bitrate.SampleRecorder
	)
	if os.Getenv("AISOTROPY_BITRATE_BIN") != "" {
		bs, berr := openBitrateStore() //nolint:contextcheck // bitrate.OpenStore has no context parameter (internal/bitrate, out of scope for this cleanup)
		if berr != nil {
			_ = wm.Close()
			_ = factsStore.Close()
			_ = semanticStore.Close()
			return nil, berr
		}
		bitrateStore = bs
		bptSampler = bs
	}

	// factMinImportance (ADR-0017 open question #2, now configurable via
	// AISOTROPY_FACT_MIN_IMPORTANCE): the same gate value threads into every
	// engram.Store facade this App builds, so "a fact and its Fact-kind
	// vector live and die together" is decided consistently regardless of
	// which path (consolidation sink vs. sleep-cycle dedup) constructed the
	// facade.
	factMinImportance := factMinImportanceFromEnv()

	// The Fact-kind aggregate facade (ADR-0017 phase 2): the sole owner of
	// "a fact and its Fact-kind vector live and die together". Every
	// Fact-kind vector mutation — from consolidation's fact writes to the
	// sleep cycle's dedup deletes below — routes through this facade, never
	// semanticStore directly.
	factEngram := engram.Store{Facts: factsStore, Semantic: semanticStore, Embedder: embedder, MinImportance: factMinImportance}

	// Consolidation sink (Phase 5 outbox): distils each drained event into the
	// facts + semantic tiers idempotently.
	sink := consolidation.Sink{
		Facts:         factsStore,
		Extractor:     factsExtractor,
		Semantic:      semanticStore,
		Embedder:      embedder,
		MinImportance: factMinImportance,
	}

	// Cold-boot reconciliation, then the steady-state worker. workerCtx is
	// deliberately NOT deferred-cancelled here (as it was inline in Run): the
	// returned close() owns stopping the workers, so they keep draining until
	// teardown regardless of which entry point booted the runtime.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	// workerWG tracks every background worker started on workerCtx so closeFn can
	// WAIT for them to observe stopWorker() and exit BEFORE closing the stores they
	// read/write. Without this join a worker mid-operation races Close and panics
	// ("DB Closed"): launchWorker wraps each goroutine to signal Done on return.
	var workerWG sync.WaitGroup
	launchWorker := func(fn func()) {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			fn()
		}()
	}
	if n, rerr := wm.RecoverOutbox(ctx, sink); rerr != nil {
		slog.Error("cold-boot outbox reconciliation failed", "err", rerr)
	} else if n > 0 {
		slog.Info("cold-boot outbox reconciliation drained orphaned events", "count", n)
	}
	launchWorker(func() { wm.RunOutboxWorker(workerCtx, sink, outboxPollInterval) }) //nolint:contextcheck // workerCtx intentionally outlives ctx (which is cancelled on signal) so this worker keeps draining until stopWorker() during graceful shutdown

	// OTel tracing (ADR-0010, no-op by default), circuit breaker, and the OTel
	// decorator shared by the streaming and reflection paths.
	shutdownTracing, tracer, tracerProvider := llm.NewTracerProvider(context.Background()) //nolint:contextcheck // tracer lifetime must outlive ctx (cancelled on signal): shutdownTracing is invoked later, during graceful shutdown, with its own bounded context

	// User model middleware (ADR-0062, docs/decisions/0062-bounded-model-
	// middleware.md): installed at the FIXED INNER slot — wrapping ONLY the raw
	// llmClient, strictly BEFORE (i.e. inside) the runtime-owned circuit breaker
	// and OTel decorators below. Because it is composed here, every downstream
	// consumer of protectedClient/streamClient (Register, RegisterStream,
	// RegisterAgent, RegisterAsk, RegisterAISDK, RegisterAGUI, and App.Turn — see
	// their call sites further down) inherits it uniformly, and because the
	// handler-level pre-spend quota gate (internal/api/core.go's chatCheckQuota,
	// internal/api/stream_core.go's inline check) runs entirely BEFORE any of
	// those handlers ever calls a Client method, a registered middleware has no
	// way to reach — let alone reorder or bypass — either constitutional gate.
	// a.ext.modelMiddleware empty (the default, no WithModelMiddleware calls)
	// makes WithModelMiddleware return llmClient completely unwrapped, so this
	// line is a no-op and the runtime is byte-for-byte unchanged from before
	// ADR-0062.
	middlewaredClient := llm.WithModelMiddleware(llmClient, a.ext.modelMiddleware...)
	protectedClient := llm.WithCircuitBreaker(middlewaredClient, circuitFailureThreshold, circuitCooldown)
	streamClient := llm.WithOTel(protectedClient, tracer)

	// Native-tool-calling capability check (asserted on the FINAL client the
	// agent route uses — protectedClient becomes rt.client, and chat_agent.go's
	// handler does exactly this `client.(llm.ToolCaller)` assertion). The
	// runtime-owned decorators above (middleware/breaker/OTel) each PRESERVE an
	// inner ToolCaller, so a warning here means the client the CONSUMER injected
	// (WithLLM) — or the env default — is StreamChat-only. That silently downgrades
	// /chat/agent (and every graph node) to the agentloop JSON fallback instead of
	// native function-calling. One log line, no behavior change: it is the signal
	// that would have surfaced a hand-rolled StreamChat-only injected client the
	// day it was wired. No secret is logged (only the client's Name()).
	if !clientSupportsToolCalling(protectedClient) {
		slog.Warn("injected LLM client does not implement native tool-calling (llm.ToolCaller); /chat/agent will use the agentloop JSON fallback instead of native function-calling — wrap with care (a StreamChat-only decorator drops this capability), or use aisotropy.NewOpenAICompatLLM", "client", protectedClient.Name())
	}

	// Optional cheap-model client for quota-pressure degradation (Phase 7). The
	// SAME user model middleware is applied here too (ADR-0062): a tenant
	// degraded to the cheap model under quota pressure is still served through
	// the identical middleware chain the primary model uses, so a registered
	// middleware's view of "every model call" stays uniform even across
	// degradation — never a silent gap a middleware author would have to special
	// case.
	var cheapStreamClient llm.Client
	if raw := llm.CheapClientFromEnv(); raw != nil {
		// The cheap client is a SECOND base client, so it gets the same usage
		// meter: a tenant degraded to the cheap model under quota pressure is
		// still real provider spend, and App.Usage()'s all-calls total must not
		// silently exclude it.
		metered := llm.WithUsageMeter(raw, a.usageAccumulator())
		cheapMiddlewared := llm.WithModelMiddleware(metered, a.ext.modelMiddleware...)
		cheapStreamClient = llm.WithOTel(llm.WithCircuitBreaker(cheapMiddlewared, circuitFailureThreshold, circuitCooldown), tracer)
		slog.Info("cheap model configured for quota-pressure degradation", "model", raw.Name())
	}

	// Sleep cycle (Phase 6): offline DataFusion consolidation when the worker
	// binary is configured.
	if bin := os.Getenv("AISOTROPY_SLEEPCYCLE_BIN"); bin != "" {
		coordinator := sleepcycle.Coordinator{
			Store:  factsStore,
			Engram: factEngram,
			Runner: sleepcycle.Worker{BinaryPath: bin, Tracer: tracer},
		}
		launchWorker(func() { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			coordinator.RunScheduler(workerCtx, sleepCycleInterval, func(s sleepcycle.Summary, serr error) {
				if serr != nil {
					slog.Error("sleep cycle failed", "err", serr)
					return
				}
				if s.Dropped > 0 {
					slog.Info("sleep cycle consolidated facts", "rows_in", s.RowsIn, "dropped", s.Dropped)
				}
			})
		})
		slog.Info("sleep-cycle worker scheduled", "bin", bin, "interval", sleepCycleInterval)
	} else {
		slog.Info("sleep-cycle worker disabled", "reason", "AISOTROPY_SLEEPCYCLE_BIN unset")
	}

	// Turn-vector bounding (ADR-0017 Phase 3) runs ALWAYS — independent of the
	// DataFusion sleep-cycle binary above. It is a pure-Go semantic-tier
	// maintenance pass; without it, raw KindTurn vectors would grow unbounded and
	// pollute retrieval. No-op when there is no semantic store or no bound set.
	if semanticStore != nil {
		pruner := sleepcycle.Coordinator{
			Store:        factsStore,
			Semantic:     semanticStore,
			TurnMaxAge:   semanticTurnTTLFromEnv(),
			TurnMaxCount: semanticTurnMaxFromEnv(),
		}
		launchWorker(func() { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			pruner.RunTurnPruneScheduler(workerCtx, turnPruneInterval, func(perr error) {
				slog.Error("turn-vector pruning failed", "err", perr)
			})
		})
		slog.Info("turn-vector pruning scheduled", "ttl", semanticTurnTTLFromEnv(), "max", semanticTurnMaxFromEnv(), "interval", turnPruneInterval)
	}

	// Per-tenant rate limiting (Phase 7 / constitutional gate). An explicit
	// WithRateLimit wins; otherwise AISOTROPY_RATE_PER_SEC / AISOTROPY_BURST
	// override the built-in defaults.
	ratePerSec, burst := a.ratePerSec, a.burst
	if !a.rateLimitSet {
		ratePerSec, burst = ratePerSecFromEnv(), burstFromEnv()
	}
	rateLimiter := wm.NewLimiter(ratePerSec, burst)

	// Connect any MCP servers (ADR-0016 lever #2: polyglot tool execution):
	// discover each server's tools and merge them into the consumer's toolset +
	// typed handlers, scope-gated. Best-effort — a server that fails to start or
	// list is logged and skipped, never downing the runtime. The runtime owns the
	// client lifecycle (clients are closed on shutdown).
	toolset := append([]tools.Tool(nil), a.toolset...)
	typedHandlers := make(map[string]api.TypedToolHandler, len(a.typedHandlers))
	for k, v := range a.typedHandlers {
		typedHandlers[k] = v
	}
	var mcpClosers []func() error
	for _, spec := range a.mcpServers {
		client, cerr := mcp.NewStdioClient(ctx, spec.command, spec.args...)
		if cerr != nil {
			slog.Error("mcp server connect failed; skipping its tools", "command", spec.command, "err", cerr)
			continue
		}
		mtools, mhandlers, berr := api.BuildMCPTools(ctx, client, spec.requiredScope)
		if berr != nil {
			slog.Error("mcp list-tools failed; skipping this server", "command", spec.command, "err", berr)
			_ = client.Close()
			continue
		}
		toolset = append(toolset, mtools...)
		for k, v := range mhandlers {
			typedHandlers[k] = v
		}
		mcpClosers = append(mcpClosers, client.Close)
		slog.Info("mcp server tools registered", "command", spec.command, "tools", len(mtools), "scope", spec.requiredScope)
	}
	closeMCP := func() {
		for _, c := range mcpClosers {
			_ = c()
		}
	}

	// Plugins (ADR-0016 ecosystem scaffolding, internal/plugin): load each
	// consumer-declared manifest — from WithPlugin file paths, from WithPluginDir
	// directories, and from the AISOTROPY_PLUGIN_DIR env directory — and merge its
	// contribution into the SAME toolset + typedHandlers the native and MCP tools
	// populate, scope-gated identically. The loader reaches an MCP subprocess only
	// through an injected connector (so internal/plugin imports no MCP client
	// library); that connector is the exact mcp.NewStdioClient + client.Close path
	// the WithMCPServer discovery loop above already uses. A plugin's subprocess
	// closer is appended to mcpClosers so graceful shutdown tears it down with the
	// MCP clients. Best-effort throughout: a manifest that fails to parse, a
	// directory that fails to scan, or a plugin that fails to load is logged and
	// skipped, never downing the runtime — matching the MCP loop's posture.
	//
	// Inert-safe: no WithPlugin/WithPluginDir and an unset AISOTROPY_PLUGIN_DIR
	// means no manifests, so toolset/typedHandlers are byte-for-byte unchanged.
	pluginDirs := append([]string(nil), a.ext.pluginDirs...)
	if envDir := strings.TrimSpace(os.Getenv("AISOTROPY_PLUGIN_DIR")); envDir != "" {
		pluginDirs = append(pluginDirs, envDir)
	}
	var pluginManifests []plugin.Manifest
	for _, path := range a.ext.pluginPaths {
		m, perr := plugin.ParseManifest(path)
		if perr != nil {
			slog.Error("plugin manifest parse failed; skipping", "path", path, "err", perr)
			continue
		}
		pluginManifests = append(pluginManifests, m)
	}
	for _, dir := range pluginDirs {
		reg, perr := plugin.Load(dir)
		if perr != nil {
			slog.Error("plugin directory scan failed; skipping", "dir", dir, "err", perr)
			continue
		}
		pluginManifests = append(pluginManifests, reg.List()...)
	}
	if len(pluginManifests) > 0 {
		// mcpConnect adapts gollem's stdio MCP client to plugin.MCPConnector — the
		// one runtime capability internal/plugin injects rather than imports.
		var mcpConnect plugin.MCPConnector = func(cctx context.Context, command string, args ...string) (api.MCPClient, func() error, error) {
			client, cerr := mcp.NewStdioClient(cctx, command, args...)
			if cerr != nil {
				return nil, nil, cerr
			}
			return client, client.Close, nil
		}
		for _, m := range pluginManifests {
			loaded, lerr := plugin.LoadTools(ctx, m, mcpConnect)
			if lerr != nil {
				slog.Error("plugin load failed; skipping its tools", "plugin", m.Name, "kind", m.Kind, "err", lerr)
				continue
			}
			toolset = append(toolset, loaded.Tools...)
			for k, v := range loaded.Handlers {
				typedHandlers[k] = v
			}
			if loaded.Close != nil {
				mcpClosers = append(mcpClosers, loaded.Close)
			}
			// Personas in a manifest are IDENTIFIERS ONLY — the YAML carries no
			// prompt body (anti-magic: a manifest cannot ship a Go func or a prompt
			// blob it expects the core to trust). NewMapPersonaProvider already
			// resolves any unregistered product_id to the built-in default persona,
			// so a declared identifier is usable with no wiring; supplying a distinct
			// prompt for it stays a WithPersona/Go concern, exactly as manifest.go
			// documents. We surface the declaration so an operator can see it, and
			// deliberately do NOT fabricate a prompt.
			if len(m.Personas) > 0 {
				slog.Info("plugin declares persona identifiers (metadata only; supply prompt bodies via WithPersona)", "plugin", m.Name, "personas", m.Personas)
			}
			slog.Info("plugin tools registered", "plugin", m.Name, "kind", m.Kind, "tools", len(loaded.Tools), "handlers", len(loaded.Handlers))
		}
	}

	// Reranker (WithReranker): APPLIED. The consumer's reranker is threaded onto
	// the runtime (rt.reranker below) and consulted by the semantic hydration path
	// — RegisterStream (/chat/stream), RegisterAsk (/ask), and the bitrate
	// hydrator above — over an isolation-clean, over-fetched candidate pool
	// (ADR-0033, internal/api's searchSemanticContext). A nil reranker preserves
	// the built-in score ordering exactly.
	if a.ext.reranker != nil {
		slog.Info("custom reranker installed (WithReranker); applied on the semantic hydration path (streaming /chat/stream, /ask, bitrate)")
	}

	// Scope-gated tool registry: the consumer's registered tools plus any
	// MCP-discovered and plugin-contributed tools.
	toolRegistry := tools.NewRegistry(toolset...)

	// Persona provider composed over the consumer's registered personas, with the
	// built-in default persona as the fallback for unknown product ids.
	persona := api.NewMapPersonaProvider(a.personas)

	// Tool executor: the consumer's, or the demo echo executor as a zero-config
	// default so the agent endpoint works out of the box.
	executor := a.executor
	if executor == nil {
		executor = api.DemoExecutor{}
	}

	// Per-tenant cost meter, exposed at GET /costs (the route is wired in Run).
	costMeter := costmeter.New()

	// Bitrate (Bet 6): when the store is enabled, schedule the offline measurement
	// (the /bitrate ops route is wired in Run). The coordinator hydrates each
	// held-out sample's context THROUGH the api hydrator seam (the exact
	// request-path hydration — anti-magic, ADR-0002), scores with/without memory
	// in the scorer subprocess, and persists the lift.
	if bitrateStore != nil {
		bin := os.Getenv("AISOTROPY_BITRATE_BIN")
		coordinator := bitrate.Coordinator{
			Sampler: bitrateStore,
			Hydrator: api.BitrateHydrator{
				Store:    factsStore,
				Semantic: semanticStore,
				Embedder: embedder,
				Reranker: a.ext.reranker,
				Persona:  persona,
			},
			Runner: bitrate.Worker{BinaryPath: bin, Tracer: tracer},
			Store:  bitrateStore,
		}
		launchWorker(func() { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			coordinator.RunScheduler(workerCtx, bitrateInterval, func(ms []bitrate.Measurement, merr error) {
				if merr != nil {
					slog.Error("bitrate measurement failed", "err", merr)
					return
				}
				if len(ms) > 0 {
					slog.Info("bitrate measured memory lift", "samples", len(ms))
				}
			})
		})
		slog.Info("bitrate scorer scheduled", "bin", bin, "interval", bitrateInterval)
	} else {
		slog.Info("bitrate scorer disabled", "reason", "AISOTROPY_BITRATE_BIN unset")
	}

	// Guardrails (redact + detect ALWAYS; blocking OPT-IN per operator toggles,
	// default off): one process-wide aggregator threaded into every chat handler.
	// The env toggles gate only whether a suspicious verdict rejects the
	// request/response — redaction and detection run regardless.
	blockOutput := guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT")

	// The output-inspection tier is assembled from up to three sources, appended
	// in this order:
	//
	//  1. the consumer's own WithGuardrailCheck checks (Wave 2), and then
	//  2. an OPTIONAL LLM-as-a-judge Safety check whose judge is EITHER the
	//     consumer's WithEvalJudge (when set) OR — only when no consumer judge was
	//     supplied — the built-in eval.NewLLMJudge, itself gated on the opt-in
	//     AISOTROPY_GUARDRAILS_LLM_JUDGE toggle and an available LLM client.
	//
	// The judge attaches through the api.EvalOutputCheck adapter over the
	// guardrails.OutputCheck seam, so internal/guardrails still never imports
	// internal/eval. The built-in judge is driven by the RAW llmClient —
	// deliberately NOT the circuit-broken chat client — so a flaky judge cannot
	// trip the chat breaker. Its error posture tracks blockOutput (fail-open when
	// annotating, fail-closed when hard-blocking); see api.NewEvalOutputCheck.
	//
	// Inert-safe: with no consumer checks, no consumer judge, and the env toggle
	// off, outputChecks stays empty and guardrailsG is guardrails.Default() —
	// byte-for-byte the pre-Wave-2 keyless/stub default.
	outputChecks := append([]guardrails.OutputCheck(nil), a.ext.guardrailChecks...)

	judge := a.ext.evalJudge
	consumerJudge := judge != nil
	if judge == nil && guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_LLM_JUDGE") && llmClient != nil {
		if j, jerr := eval.NewLLMJudge(llmClient, eval.Safety, eval.SafetyRubric, 0); jerr != nil {
			slog.Error("guardrails LLM judge disabled: judge construction failed; using built-in output heuristics only", "err", jerr)
		} else {
			judge = j
		}
	}
	if judge != nil {
		if check, cerr := api.NewEvalOutputCheck(judge, guardrailsJudgeSafetyThreshold, blockOutput); cerr != nil {
			slog.Error("guardrails LLM judge disabled: adapter construction failed; using built-in output heuristics only", "err", cerr)
		} else {
			outputChecks = append(outputChecks, check)
			slog.Info("guardrails LLM safety judge attached to the output inspector",
				"threshold", guardrailsJudgeSafetyThreshold, "fail_closed", blockOutput, "consumer_supplied", consumerJudge)
		}
	}

	// The Detector optionally layers the opt-in multilingual model scanner
	// (ADR-0052/0057) behind the regex detector via AISOTROPY_GUARDRAIL_MODEL_BIN;
	// unset ⇒ DetectorFromEnv() == NewDetector(), byte-for-byte the prior default.
	guardDetector := guardrails.DetectorFromEnv()
	guardrailsG := guardrails.New(guardrails.Config{Detector: guardDetector})
	if len(outputChecks) > 0 {
		inspector := guardrails.NewOutputInspector(nil).WithChecks(outputChecks...)
		guardrailsG = guardrails.New(guardrails.Config{Detector: guardDetector, Output: inspector})
	}

	// One process-wide aggregator threaded into every chat handler. The block
	// toggles gate only whether a suspicious verdict rejects the request/replaces
	// the response — redaction and detection run regardless.
	//
	// SAFER-BY-DEFAULT POSTURE (Gap 7): injection BLOCKING now defaults ON
	// (opt-OUT). A prompt whose injection verdict is Suspicious is rejected unless
	// an operator explicitly sets AISOTROPY_GUARDRAILS_BLOCK_INJECTION=false (e.g.
	// for local dev / eval harnesses that intentionally send jailbreak strings).
	// This governs BOTH the live-prompt gate and the knowledge-ingest gate. Output
	// blocking stays OPT-IN OFF because hard-blocking the streaming route requires
	// buffering the whole response (a latency tradeoff, see RegisterStream), and
	// the LLM-as-judge output check stays OPT-IN OFF for cost. Every default is
	// overridable via its env toggle; see docs/security/ai-layer-guardrails.md.
	guard := &api.Guard{
		G:              guardrailsG,
		BlockInjection: guardrailsBlockFromEnvDefault("AISOTROPY_GUARDRAILS_BLOCK_INJECTION", true),
		BlockOutput:    blockOutput,
	}
	slog.Info("guardrails wired (redact+detect always on)", "block_injection", guard.BlockInjection, "block_output", guard.BlockOutput)

	// Model-selection gateway (the seam): default target -> primary; a
	// cost-budget policy shapes an over-budget tenant to the cheap target when
	// BOTH a budget (AISOTROPY_COST_BUDGET_USD) and a cheap client are
	// configured — otherwise the rule is skipped cleanly and the gateway is a
	// pass-through to primary. Deliberately NO prompt-length rule (Bet-4 refuted
	// length as a routing predictor); request-shape rules (image/UI override)
	// are ready policy but never fire because ChatInput carries no such signal
	// yet. The gateway's primary is the OTel-instrumented streaming client, so
	// routing the non-streaming reflection path through it additionally
	// instruments that path (a no-op unless a tracer is configured). The
	// streaming path's quota-pressure degrade (llm.DegradeAt) still applies as
	// an override AFTER the gateway pick.
	modelGateway := buildModelGateway(streamClient, cheapStreamClient, costBudgetFromEnv(), visionModelFromEnv(), a.ext.modelPolicies)

	// Per-user primitives (Phase 6 behavioral memory). The consolidation loop is
	// a background worker; the friction detector is consumed by the agent route
	// (wired in Run).
	primStore := primitives.NewStore()
	frictionDetector := primitives.NewFrictionDetector(primStore)
	launchWorker(func() { runPrimitiveConsolidation(workerCtx, primStore) }) //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown

	rt := &appRuntime{
		client:            protectedClient,
		wm:                wm,
		factsStore:        factsStore,
		limiter:           rateLimiter,
		toolRegistry:      toolRegistry,
		persona:           persona,
		costMeter:         costMeter,
		gateway:           modelGateway,
		guard:             guard,
		semanticStore:     semanticStore,
		embedder:          embedder,
		reranker:          a.ext.reranker,
		streamClient:      streamClient,
		cheapStreamClient: cheapStreamClient,
		typedHandlers:     typedHandlers,
		executor:          executor,
		frictionDetector:  frictionDetector,
		tracerProvider:    tracerProvider,
		bitrateStore:      bitrateStore,
		bptSampler:        bptSampler,
	}
	// closeFn is the exact teardown Run's graceful shutdown ran inline: stop the
	// workers BEFORE closing the stores they write to, close the MCP clients, then
	// shut tracing on the caller's bounded context. Each store-close error is
	// logged, never fatal (a shutdown never blocks on a failed close).
	rt.closeFn = func(shutdownCtx context.Context) {
		stopWorker()
		// Join the background workers before closing the stores they touch: a
		// worker still mid-operation when a store closes panics ("DB Closed"). The
		// wait is bounded by shutdownCtx so a stuck worker never blocks teardown
		// forever — on timeout we close best-effort, exactly as before this join.
		waitWorkers(shutdownCtx, &workerWG)
		closeMCP()
		if cerr := wm.Close(); cerr != nil {
			slog.Error("working memory close failed", "err", cerr)
		}
		if cerr := factsStore.Close(); cerr != nil {
			slog.Error("facts memory close failed", "err", cerr)
		}
		if cerr := semanticStore.Close(); cerr != nil {
			slog.Error("semantic memory close failed", "err", cerr)
		}
		if bitrateStore != nil {
			if cerr := bitrateStore.Close(); cerr != nil {
				slog.Error("bitrate store close failed", "err", cerr)
			}
		}
		if cerr := shutdownTracing(shutdownCtx); cerr != nil { //nolint:contextcheck // ctx is already cancelled at this point; shutdownCtx deliberately gives tracer shutdown its own bounded deadline
			slog.Error("otel tracer shutdown failed", "err", cerr)
		}
	}
	return rt, nil
}

// Run boots the full runtime and blocks until graceful shutdown. It internalizes
// exactly what cmd/aisotropyd/main.go did by hand: working memory, tier routing,
// facts + semantic stores, the consolidation sink + outbox worker (+ cold-boot
// reconciliation), OTel tracing, the LLM circuit breaker, cheap-model
// degradation, the sleep-cycle worker, per-tenant rate limiting, the cost meter,
// the /healthz + /health + /costs endpoints, the /v1 chat routes (native,
// streaming, agent, plus the Vercel AI-SDK and AG-UI protocol-adapter costumes
// over the same streaming pipeline), per-user primitive friction consolidation,
// and SIGINT/SIGTERM graceful shutdown that closes every store.
//
// ctx cancellation, SIGINT, or SIGTERM all trigger graceful shutdown. Run
// returns nil on a clean shutdown and a non-nil error only if the HTTP server
// itself fails to start/serve.
//
//nolint:gocognit,cyclop,funlen // large cohesive HTTP wiring; runtime construction now lives in startRuntime, refactor tracked in docs/architecture/phases-3-7-as-built.md debt ledger
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := ResolveListenAddr(a.addr)

	// Build the shared runtime core (stores, model pipeline, tools, guardrails,
	// gateway, cost meter, rate limiter, background workers) exactly as before —
	// the construction moved verbatim into startRuntime, which Turn also boots.
	// Everything below is the HTTP surface layered ON TOP of it. Run builds its
	// own runtime local to this call (never the App's Turn-memoized one), so it
	// owns the returned rt's teardown outright.
	rt, err := a.startRuntime(ctx)
	if err != nil {
		return err
	}

	// Access-audit sink (Gap 8): the durable, structured who-accessed-what-tenant-
	// when trail the /v1 audit middleware writes to. WithAuditSink wins; otherwise
	// audit.FromEnv selects a durable JSONL file (AISOTROPY_AUDIT_LOG) or the
	// dedicated slog "access_audit" logger (the always-on default). A set-but-
	// unopenable AISOTROPY_AUDIT_LOG FAILS the boot (fail closed): an operator who
	// asked for a retained audit file must not silently run without it. The sink is
	// closed (fsync + close) on graceful shutdown, after the server has drained.
	auditSink := a.auditSink
	if auditSink == nil {
		auditSink, err = audit.FromEnv()
		if err != nil {
			rt.close(context.Background()) //nolint:contextcheck // startRuntime succeeded; tear its runtime down on a fresh ctx before returning the audit-sink boot error
			return err
		}
	}
	defer func() {
		if cerr := auditSink.Close(); cerr != nil {
			slog.Error("access-audit sink close failed", "err", cerr)
		}
	}()

	router := chi.NewRouter()
	// Opt-in CORS (AISOTROPY_CORS_ALLOWED_ORIGINS): installed as the OUTERMOST
	// middleware so a browser preflight (OPTIONS) is answered and short-circuited
	// here — before the /v1 auth gate and before any route — and so the
	// Access-Control-Allow-Origin header lands on EVERY response, including the
	// SSE (text/event-stream) streams. Secure default: with the env var unset the
	// allowlist is empty and this is a pass-through emitting no CORS headers
	// (same-origin only); it never reflects "*". See cors.go and
	// docs/deployment/reverse-proxy.md.
	router.Use(newCORSMiddleware(corsAllowlistFromEnv()))
	router.Use(middleware.RequestID)
	// Access audit runs just inside RequestID (so the correlation id is set) and
	// OUTSIDE Recoverer, so a request whose handler panics is still audited — the
	// panic is recovered below into a 500 that this middleware records on the way
	// out. It wraps the ResponseWriter without buffering, so SSE streaming is
	// unaffected (see internal/api/audit_middleware.go).
	router.Use(api.AuditMiddleware(auditSink))
	router.Use(middleware.Recoverer)
	// Emit nginx's `X-Accel-Buffering: no` on the token-by-token SSE routes so the
	// stream is not batched by a default-configured (proxy_buffering on) reverse
	// proxy in front of aisotropyd. Defense in depth: the reference
	// deploy/proxy/nginx.conf also disables buffering, but this header makes the
	// stream work even if the operator forgets. See sse_buffering.go and
	// docs/deployment/reverse-proxy.md.
	router.Use(sseAntiBuffering)

	// Optional TLS enforcement (AISOTROPY_REQUIRE_TLS truthy): reject any request
	// that did not arrive over TLS (r.TLS == nil) so a bearer token — the /v1 JWT
	// or the AISOTROPY_OPS_TOKEN — can never be transmitted in plaintext. OFF by
	// default: most deployments sit behind a proxy/load balancer that terminates
	// TLS upstream and forwards plain HTTP, where r.TLS is legitimately nil. Turn
	// it on only when aisotropyd terminates TLS itself. Registered as a Use()
	// middleware BEFORE any route, so chi accepts it and it covers every path.
	if requireTLSFromEnv() {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil {
					http.Error(w, `{"error":"TLS required"}`, http.StatusBadRequest)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
	}

	// Liveness, outside the OpenAPI contract.
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Built-in development chat UI (WithDevUI / WithDevUIAt, aisotropy/devui.go),
	// registered ONLY when opted in. Like /healthz it is a non-/v1 chi route, so
	// it sits OUTSIDE the /v1 authn middleware installed below — which is exactly
	// why it is a development convenience and NOT a production surface (see the
	// WithDevUI doc). Inert by default: an empty devUIPath registers nothing, so
	// GET /ui 404s in a stock build.
	if a.devUIPath != "" {
		router.Get(a.devUIPath, devUIHandler())
		slog.Warn("development chat UI enabled (UNAUTHENTICATED, single-page — development convenience, NOT for production; build your own frontend on the /chat/agui or /chat/aisdk adapters for production)", "path", a.devUIPath)
	}

	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy", "0.1.0"))

	// Authenticated-identity seam (the single fail-closed gate in front of every
	// /v1 route): installed as ONE Huma API middleware BEFORE any route is
	// registered below. Resolve the authenticator: nil -> default-deny (Option
	// C) so an unconfigured runtime rejects every /v1 request rather than
	// silently trusting path identity; else bind the authenticator with
	// must-agree semantics. The health/liveness/costs/openapi/docs routes live
	// on chi (or the Huma adapter's Handle), outside this middleware chain, so
	// they stay open.
	if a.authenticator == nil {
		humaAPI.UseMiddleware(api.DenyAllMiddleware(humaAPI))
		slog.Warn("no authenticator configured: all /v1 requests are rejected 401 (default-deny). Pass aisotropy.WithAuthenticator; use aisotropy.NoAuth() to explicitly disable.")
	} else {
		humaAPI.UseMiddleware(api.BindAuthMiddleware(humaAPI, a.authenticator))
	}

	// Operator-endpoint gate (default-closed): /costs and /bitrate return
	// ALL-tenant aggregates, so they live behind a shared operator token
	// (AISOTROPY_OPS_TOKEN) instead of the per-tenant /v1 JWT gate. With the
	// token unset every request gets 503; with it set the request must carry a
	// matching "Authorization: Bearer <token>" (401 otherwise). Health/liveness/
	// openapi/docs stay open.
	opsGate := api.OpsAuthGate(api.OpsTokenFromEnv())

	// Per-tenant cost meter (built in startRuntime), exposed at GET /costs.
	router.With(opsGate).Get("/costs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rt.costMeter.Snapshot())
	})

	// Bitrate (Bet 6) ops view: expose the per-tenant lift summary when the store
	// is enabled (the offline measurement scheduler is started in startRuntime).
	if rt.bitrateStore != nil {
		router.With(opsGate).Get("/bitrate", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			summaries, serr := rt.bitrateStore.AllSummaries(r.Context())
			if serr != nil {
				http.Error(w, `{"error":"bitrate summary failed"}`, http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(summaries)
		})
	}

	// Readiness: cheap, read-only, health-tenant-scoped probes of the networked
	// tiers. No probe spends LLM tokens.
	router.Get("/health", health.New(readinessProbeTimeout,
		health.Probe{Name: "facts", Check: func(ctx context.Context) error {
			sc, serr := facts.NewScope(healthProbeTenant, healthProbeTenant) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
			if serr != nil {
				return serr
			}
			_, serr = rt.factsStore.Search(ctx, sc, "health", 1)
			return serr
		}},
		health.Probe{Name: "semantic", Check: func(ctx context.Context) error {
			sc, serr := semantic.NewScope(healthProbeTenant, healthProbeTenant) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
			if serr != nil {
				return serr
			}
			_, serr = rt.semanticStore.Search(ctx, sc, healthProbeVector(rt.embedder.Dimension()), 1)
			return serr
		}},
	).Handler())

	// MCP SERVE (opt-in via WithMCPServe): publish this runtime's scope-gated
	// tools as a Model Context Protocol server. A chi route OUTSIDE the /v1 Huma
	// middleware — it runs its OWN per-request authentication (the same
	// Authenticator) and derives the served tool set from the caller's VERIFIED
	// scopes, so only Permitted(scopes) tools are ever listed or callable, each
	// served call dispatches through the same tenant-scoped executor path, and the
	// per-tenant quota gate still applies. Inert by default (nil => not mounted, so
	// the runtime stays MCP consume-only).
	if a.mcpServe != nil {
		router.Handle(a.mcpServe.path, a.buildMCPServeHandler(rt))
		slog.Info("MCP serve endpoint enabled (scope-gated tool publishing)", "path", a.mcpServe.path)
	}

	api.Register(humaAPI, rt.client, rt.wm, rt.factsStore, rt.limiter, rt.toolRegistry, rt.persona, rt.costMeter, rt.gateway, rt.guard)
	api.RegisterStream(humaAPI, rt.streamClient, rt.wm, rt.factsStore, rt.limiter, rt.toolRegistry, rt.costMeter, rt.cheapStreamClient, rt.semanticStore, rt.embedder, rt.reranker, rt.persona, a.inPricePer1M, a.outPricePer1M, rt.bptSampler, rt.gateway, rt.guard)
	// Frontend-UI protocol adapters (Phase 2A/2B): the Vercel AI SDK "UI Message
	// Stream" and the AG-UI (CopilotKit) event stream. Both are PROTOCOL COSTUMES
	// over the exact same StreamCore pipeline RegisterStream drives — their
	// parameter lists mirror RegisterStream one-for-one and they are handed the
	// IDENTICAL dependencies here, so they mount on the same /v1 path behind the
	// same authn middleware + must-agree identity bind and re-encode nothing but
	// the output wire format. Tenant/user isolation, the quota gate, guardrails,
	// tenant-scoped hydration, and cost accounting are all inside StreamCore, so
	// the adapters cannot drift from the native endpoint's constitutional
	// guarantees (see internal/api/aisdk.go, internal/api/agui.go).
	api.RegisterAISDK(humaAPI, rt.streamClient, rt.wm, rt.factsStore, rt.limiter, rt.toolRegistry, rt.costMeter, rt.cheapStreamClient, rt.semanticStore, rt.embedder, rt.reranker, rt.persona, a.inPricePer1M, a.outPricePer1M, rt.bptSampler, rt.gateway, rt.guard)
	api.RegisterAGUI(humaAPI, rt.streamClient, rt.wm, rt.factsStore, rt.limiter, rt.toolRegistry, rt.costMeter, rt.cheapStreamClient, rt.semanticStore, rt.embedder, rt.reranker, rt.persona, a.inPricePer1M, a.outPricePer1M, rt.bptSampler, rt.gateway, rt.guard)
	// OpenAI Chat-Completions-compatible shim (ADR-0066): the THIRD protocol
	// costume over the same StreamCore pipeline. Unlike the two adapters above
	// it mounts OUTSIDE the tenant/user prefix (POST /v1/chat/completions) and
	// derives identity EXCLUSIVELY from the verified credential via the authn
	// middleware's principal-derived mode — a NoAuth or unauthenticated setup
	// gets 401 on this route by construction (see internal/api/openai_compat.go).
	api.RegisterOpenAICompat(humaAPI, rt.streamClient, rt.wm, rt.factsStore, rt.limiter, rt.toolRegistry, rt.costMeter, rt.cheapStreamClient, rt.semanticStore, rt.embedder, rt.reranker, rt.persona, a.inPricePer1M, a.outPricePer1M, rt.bptSampler, rt.gateway, rt.guard)
	// Knowledge ingestion (ADR-0018): the HTTP arm of the dual-surface
	// capability, sharing the exact same semantic store/embedder pair the
	// chat routes' RAG hydration reads from above.
	api.RegisterKnowledge(humaAPI, rt.semanticStore, rt.embedder, a.chunkOptions(), rt.guard)
	// Knowledge deletion (ADR-0018 open q#3): removes a document's chunks by
	// id, regardless of chunk count.
	api.RegisterKnowledgeDelete(humaAPI, rt.semanticStore)
	// Tenant-shared knowledge ingestion + deletion (ADR-0019): a corpus
	// ingested once at tenant scope, retrievable by every user of that
	// tenant via hydrateSystemPrompt's merged search. The ingest gate is wired
	// with the same guard so a poisoned tenant-shared doc is scanned/blocked.
	api.RegisterTenantKnowledge(humaAPI, rt.semanticStore, rt.embedder, a.chunkOptions(), rt.guard)
	api.RegisterTenantKnowledgeDelete(humaAPI, rt.semanticStore)

	// Stateless cached /ask route (makes internal/responsecache LIVE). It sits
	// behind the SAME authn middleware + must-agree bind as the chat routes (it is
	// a /v1 route registered on humaAPI, after UseMiddleware above) and reuses the
	// same guardrails posture. Unlike /chat it is STATELESS: no working-memory
	// hydration, no session, no consolidation outbox — only tenant-scoped semantic
	// RAG feeds the one LLM call, and the answer is cached per (tenant,user). This
	// is the SAFE use of the cache: with no memory-hydrated personalization mixed
	// in, a cached answer cannot go stale against a user's evolving working memory.
	// The cache is per-process and scoped by (tenant,user) in both layers, so a
	// cross-tenant hit is structurally impossible (see internal/responsecache).
	askCache := buildResponseCache(rt.embedder)
	api.RegisterAsk(humaAPI, rt.client, askCache, rt.semanticStore, rt.embedder, rt.reranker, rt.persona, rt.guard)

	// Agentic tool-use endpoint (Phase 7): the injected executor drives real tool
	// logic, wrapped per-request in a friction recorder.
	// Typed-tool dispatcher (ADR-0016 lever #1): raw-JSON handlers registered via
	// WithTypedTool[T]. Preferred over the map executor for any tool it owns; nil
	// handlers => an executor that owns nothing, so behavior is unchanged.
	typedExecutor := api.NewTypedToolExecutor(rt.typedHandlers)
	// Close the primitives self-improvement read loop (ADR-0053 follow-on): the
	// agent handler overlays each (tenant,user)'s friction-rewritten tool guidance
	// onto the base tool metadata. nil-safe — with no store the behavior is
	// byte-for-byte unchanged.
	api.RegisterAgent(humaAPI, rt.client, rt.toolRegistry, rt.executor, typedExecutor, rt.frictionDetector, rt.tracerProvider, agentMaxSteps, rt.guard, rt.frictionDetector.Store())

	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("aisotropyd starting", "addr", addr, "llm", rt.client.Name(), "openapi", "/openapi.json", "docs", "/docs")
		if serr := server.ListenAndServe(); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			serverErrCh <- serr
			return
		}
		serverErrCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections", "timeout", shutdownTimeout)
	case serr := <-serverErrCh:
		if serr != nil {
			slog.Error("server stopped", "err", serr)
			// Server failed to start: tear the runtime down on a fresh context
			// (ctx is already cancelled) and surface the error. rt.close runs the
			// exact worker-stop → store-close → tracer-shutdown sequence Run used
			// to inline here.
			rt.close(context.Background()) //nolint:contextcheck // ctx is already cancelled here (server failed to start after a shutdown signal); teardown deliberately uses a fresh context
			return serr
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if serr := server.Shutdown(shutdownCtx); serr != nil { //nolint:contextcheck // ctx is already cancelled at this point (draining triggered by ctx.Done()); shutdownCtx deliberately gives the drain its own bounded deadline
		slog.Error("server shutdown did not complete cleanly", "err", serr)
	}

	// Stop the workers and close every store/tracer, on the bounded shutdown
	// deadline. rt.close is the single teardown path (also used by App.Close for a
	// Turn-built runtime); it stops workers before closing the stores they write
	// to, exactly as before.
	rt.close(shutdownCtx)

	slog.Info("aisotropyd stopped")
	return nil
}

// runPrimitiveConsolidation periodically rewrites per-user primitives whose
// accrued friction crossed the threshold (Phase 6 sleep cycle).
func runPrimitiveConsolidation(ctx context.Context, store *primitives.Store) {
	ticker := time.NewTicker(sleepCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := primitives.Consolidate(ctx, store, primitives.HeuristicRewriter{}, primitiveFrictionThreshold)
			if err != nil {
				slog.Error("primitive consolidation failed", "err", err)
			} else if n > 0 {
				slog.Info("primitives rewritten under friction", "count", n)
			}
		}
	}
}

// healthProbeVector returns a cheap, valid unit query vector of the embedder's
// dimension for the semantic readiness probe.
func healthProbeVector(dim int) []float32 {
	v := make([]float32, dim)
	if dim > 0 {
		v[0] = 1
	}
	return v
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// openFactsStore builds the facts store, applying ADR-0007 tenant-tier routing
// on the SQLite backend. rqlite dedicated tenants would need a per-tenant
// cluster (infra), so routing is not applied there.
func openFactsStore(router routing.Router, dedicated []string) (facts.Store, error) {
	if os.Getenv("RQLITE_URL") != "" {
		if len(dedicated) > 0 {
			slog.Warn("facts routing not applied on the rqlite backend (dedicated tenant = separate cluster, infra); using the shared pool", "dedicated", dedicated)
		}
		return facts.OpenFromEnv()
	}
	dataDir := os.Getenv("AISOTROPY_DATA_DIR")
	if dataDir != "" {
		// Honest at-rest posture (Gap 5): the pure-Go modernc.org/sqlite driver has
		// no SQLCipher/PRAGMA key, so the on-disk facts DB is NOT app-encrypted. We
		// do not fake a PRAGMA — the accepted compensating control is operator
		// disk/volume encryption (LUKS/dm-crypt/cloud-EBS). See ADR-0045.
		slog.Warn("facts SQLite tier is on-disk and NOT app-encrypted at rest (the pure-Go driver has no SQLCipher): rely on operator disk/volume encryption (LUKS/dm-crypt/cloud-EBS) as the compensating control — see ADR-0045", "data_dir", dataDir)
	}
	factory := func(resource string) (facts.Store, error) {
		if dataDir == "" {
			return facts.OpenSQLite(":memory:")
		}
		return facts.OpenSQLite(filepath.Join(dataDir, resource+".db"))
	}
	if len(dedicated) == 0 {
		return factory("facts")
	}
	return facts.NewRoutingStore(router, "facts", dedicated, factory)
}

// semanticTurnTTLFromEnv parses AISOTROPY_SEMANTIC_TURN_TTL (a Go duration,
// e.g. "168h") for the sleep cycle's Turn-kind age-TTL policy (ADR-0017
// phase 3). Falls back to defaultSemanticTurnTTL when unset, unparsable, or
// non-positive, so the policy is always bounded, never accidentally
// unbounded from a bad env value.
func semanticTurnTTLFromEnv() time.Duration {
	if v := os.Getenv("AISOTROPY_SEMANTIC_TURN_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		slog.Warn("invalid AISOTROPY_SEMANTIC_TURN_TTL; using default", "value", v, "default", defaultSemanticTurnTTL)
	}
	return defaultSemanticTurnTTL
}

// semanticTurnMaxFromEnv parses AISOTROPY_SEMANTIC_TURN_MAX (an integer
// count) for the sleep cycle's Turn-kind per-(tenant,user) count-cap policy
// (ADR-0017 phase 3). Falls back to defaultSemanticTurnMax when unset,
// unparsable, or non-positive.
func semanticTurnMaxFromEnv() int {
	if v := os.Getenv("AISOTROPY_SEMANTIC_TURN_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		slog.Warn("invalid AISOTROPY_SEMANTIC_TURN_MAX; using default", "value", v, "default", defaultSemanticTurnMax)
	}
	return defaultSemanticTurnMax
}

// factMinImportanceFromEnv parses AISOTROPY_FACT_MIN_IMPORTANCE (a float) for
// engram.Store.MinImportance (ADR-0017 open question #2 / phase 2's
// Fact-kind dual-indexing gate): a fact's content is embedded into a
// Fact-kind vector only when its Importance is >= this threshold. Falls back
// to defaultFactMinImportance (0.0, i.e. always dual-index — no behavior
// change from before this was configurable) when unset or unparsable. A
// deployment can raise this to bound embedding cost by skipping
// low-importance facts.
func factMinImportanceFromEnv() float64 {
	if v := os.Getenv("AISOTROPY_FACT_MIN_IMPORTANCE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		slog.Warn("invalid AISOTROPY_FACT_MIN_IMPORTANCE; using default", "value", v, "default", defaultFactMinImportance)
	}
	return defaultFactMinImportance
}

// ratePerSecFromEnv parses AISOTROPY_RATE_PER_SEC (a float, requests/sec) for
// the per-tenant token-bucket rate limiter (Phase 7). Falls back to
// defaultRatePerSec when unset, unparsable, or non-positive, so the limiter is
// never constructed with a zero or negative rate from a bad env value.
func ratePerSecFromEnv() float64 {
	if v := os.Getenv("AISOTROPY_RATE_PER_SEC"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
		slog.Warn("invalid AISOTROPY_RATE_PER_SEC; using default", "value", v, "default", defaultRatePerSec)
	}
	return defaultRatePerSec
}

// requireTLSFromEnv reports whether AISOTROPY_REQUIRE_TLS is set to a truthy
// value (per strconv.ParseBool: "1", "t", "true", etc.). It gates the optional
// plaintext-rejection middleware. Unset, empty, or unparsable => false (off),
// so the default posture is unchanged for behind-proxy deployments.
func requireTLSFromEnv() bool {
	v := os.Getenv("AISOTROPY_REQUIRE_TLS")
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err == nil && on
}

// burstFromEnv parses AISOTROPY_BURST (an integer bucket capacity) for the
// per-tenant token-bucket rate limiter (Phase 7). Falls back to defaultBurst
// when unset, unparsable, or non-positive.
func burstFromEnv() float64 {
	if v := os.Getenv("AISOTROPY_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return float64(n)
		}
		slog.Warn("invalid AISOTROPY_BURST; using default", "value", v, "default", defaultBurst)
	}
	return defaultBurst
}

// openBitrateStore builds the Bet 6 bpt store: on-disk SQLite under
// AISOTROPY_DATA_DIR when set, else in-memory. Tenant isolation is per-statement
// (every read/write filters by tenant_id+user_id through internal/tenancy), so no
// tier routing is applied — a dedicated tenant's bpt rows are isolated by that
// filter, not by a separate physical store.
func openBitrateStore() (*bitrate.Store, error) {
	dataDir := os.Getenv("AISOTROPY_DATA_DIR")
	if dataDir == "" {
		return bitrate.OpenStore(":memory:")
	}
	return bitrate.OpenStore(filepath.Join(dataDir, "bitrate.db"))
}

// openSemanticStore builds the semantic store, applying ADR-0007 routing so each
// dedicated tenant gets its own Qdrant collection / in-memory index.
func openSemanticStore(ctx context.Context, router routing.Router, dedicated []string, dim int) (semantic.Store, error) {
	base := os.Getenv("QDRANT_COLLECTION")
	if base == "" {
		base = "aisotropy_knowledge"
	}
	factory := func(resource string) (semantic.Store, error) {
		if os.Getenv("QDRANT_HOST") == "" {
			return semantic.NewInMemoryStore(dim), nil
		}
		port := 6334
		if p := os.Getenv("QDRANT_PORT"); p != "" {
			if n, perr := strconv.Atoi(p); perr == nil && n > 0 {
				port = n
			}
		}
		st, err := semantic.OpenQdrant(ctx, semantic.QdrantConfig{
			Host:       os.Getenv("QDRANT_HOST"),
			Port:       port,
			APIKey:     os.Getenv("QDRANT_API_KEY"),
			UseTLS:     os.Getenv("QDRANT_USE_TLS") == "true",
			Collection: resource,
			Dim:        dim,
		})
		if err != nil {
			// Return a genuinely-nil interface on error — NOT a typed-nil
			// (*QdrantStore)(nil) boxed into semantic.Store. A boxed typed-nil is
			// != nil as an interface, so it slips past App.Close's
			// `if semanticStore != nil` guard and panics on the nil-receiver
			// Close(). Same reasoning for NewRoutingStore below.
			return nil, err
		}
		return st, nil
	}
	if len(dedicated) == 0 {
		return factory(base)
	}
	rs, err := semantic.NewRoutingStore(router, base, dedicated, factory)
	if err != nil {
		return nil, err
	}
	return rs, nil
}

// gwPrimary / gwCheap are the gateway target NAMES the model-selection seam
// registers its two clients under (internal/gateway resolves a policy Decision's
// target name to a concrete llm.Client). Kept as constants so the registration
// and the CostAwarePolicy.Cheap target can never drift apart.
const (
	gwPrimary = "primary"
	gwCheap   = "cheap"
)

// buildModelGateway assembles the model-selection gateway seam. primary is
// always registered as the default target; cheap is registered (and the
// cost-budget shaping rule enabled) only when a cheap client exists AND a
// positive USD budget is configured — otherwise the rule is skipped cleanly and
// the gateway is a pure pass-through to primary. There is deliberately no
// prompt-length/difficulty rule (Bet-4 refuted length as a routing predictor).
//
// visionTarget is the registry NAME an image-bearing request is routed to via
// the capability-based ImageRule (chained BEFORE the cost-budget rule): empty
// means "route images to primary" (AISOTROPY_VISION_MODEL unset). See the
// ImageRule wiring below for the honest-scope caveat (the routing signal is
// wired, but image bytes are not yet transmitted to the model).
func buildModelGateway(primary, cheap llm.Client, budgetUSD float64, visionTarget string, consumerPolicies []gateway.Policy) *gateway.Gateway {
	// Consumer routing policies (WithModelPolicy) are chained FIRST. ChainPolicy
	// returns the first non-abstaining Decision, so a consumer policy that names a
	// concrete target wins over the built-in override/image/cost rules, while one
	// that abstains (empty Target) falls through to them unchanged. This ordering
	// lets a consumer express bespoke routing (per-tenant model pinning, an
	// experiment split, a compliance override) ABOVE the built-ins, and keeps the
	// built-in chain as the default for every request the consumer policies
	// decline to route. Inert-safe: an empty consumerPolicies slice yields exactly
	// the pre-Wave-2 chain (override -> image -> cost -> default).
	policies := make([]gateway.Policy, 0, len(consumerPolicies)+3)
	policies = append(policies, consumerPolicies...)

	// OverridePolicy next so an explicit caller preference (ChatRequest.
	// ModelPreference -> gateway.Request.UISelector) wins over the cost-budget
	// rule. It is inert-safe: it abstains when UISelector is empty, so requests
	// with no preference route exactly as before. The streaming path's
	// quota-pressure DegradeAt still overrides even an honored preference (the
	// safety valve), because it is applied AFTER the gateway pick (see
	// RegisterStream). An unknown preference target resolves to the default at
	// Route time, so a client value can never drop a request.
	policies = append(policies, gateway.OverridePolicy{})

	// ImageRule (capability-based routing), chained AFTER the explicit override
	// but BEFORE the cost-budget rule: an image-bearing request
	// (ChatRequest.Images present -> gateway.Request.HasImage) is routed to the
	// vision/premium target regardless of spend, because a multimodal request
	// cannot be correctly served by shaping down to the cheap text model. Target:
	// AISOTROPY_VISION_MODEL when set (it must name a registered client; an
	// unregistered name resolves safely to the default at Route time), else the
	// primary. The rule is inert-safe: a request with no image signal makes the
	// rule abstain (empty target) and the chain falls through to cost/default
	// exactly as before.
	//
	// HONEST SCOPE: this activates the ROUTING SIGNAL only. The current llm.Client
	// implementations are text-only, so the image bytes are NOT transmitted to the
	// model yet — a multimodal Client is the named follow-on. Until then this
	// ensures an image turn at least lands on the most capable configured target.
	imageTarget := visionTarget
	if imageTarget == "" {
		imageTarget = gwPrimary
	}
	policies = append(policies, gateway.RulesPolicy{Rules: []gateway.Rule{gateway.ImageRule(imageTarget)}})
	if imageTarget != gwPrimary {
		slog.Info("model gateway image routing enabled", "vision_target", imageTarget)
	}

	if cheap != nil && budgetUSD > 0 {
		policies = append(policies, gateway.CostAwarePolicy{BudgetUSD: budgetUSD, Cheap: gwCheap})
		slog.Info("model gateway cost-budget shaping enabled", "budget_usd", budgetUSD, "cheap_target", gwCheap)
	}
	gw := gateway.New(gateway.ChainPolicy{Policies: policies}, slog.Default())
	// primary is streamClient (non-nil): Register also makes it the default.
	if err := gw.Register(gwPrimary, primary); err != nil {
		slog.Error("model gateway primary registration failed; gateway will pass through", "err", err)
	}
	if cheap != nil {
		if err := gw.Register(gwCheap, cheap); err != nil {
			slog.Error("model gateway cheap registration failed", "err", err)
		}
	}
	if err := gw.SetDefault(gwPrimary); err != nil {
		slog.Error("model gateway set-default failed", "err", err)
	}
	return gw
}

// Response-cache tuning for the stateless /ask route (internal/responsecache).
// These are process-wide, in-process bounds — deliberately conservative so the
// cache is a pure win (bounded memory, TTL'd freshness) with no external
// dependency. The exact layer is always present; the semantic layer is added
// only when an embedder exists.
const (
	// askCacheTTL bounds how long an exact-cache answer stays servable.
	askCacheTTL = 1 * time.Hour
	// askCacheCapacity bounds the exact cache's LRU entry count (process-wide,
	// across all tenants — isolation is by key, not by capacity).
	askCacheCapacity = 4096
	// askSemanticThreshold is the minimum cosine similarity for a semantic-cache
	// hit. Set high so only near-identical paraphrases hit — a conservative
	// posture that avoids serving a subtly-different question's answer.
	askSemanticThreshold = 0.95
	// askSemanticTTL bounds a semantic-cache entry's lifetime.
	askSemanticTTL = 1 * time.Hour
	// askSemanticMaxPerScope bounds a single (tenant,user) partition's size, so
	// per-Get scan cost and memory stay bounded (oldest-first eviction).
	askSemanticMaxPerScope = 512
)

// buildResponseCache constructs the per-process ResponseCache for the /ask
// route: an exact (hash) layer always, plus a semantic (nearest-neighbor) layer
// when an embedder is available so paraphrases also hit. Both layers key/
// partition by (tenant,user), so a cross-tenant hit is impossible by
// construction. A semantic-layer construction failure degrades to exact-only
// (logged) rather than failing the runtime — the exact layer is the cheaper,
// higher-confidence line of defense.
func buildResponseCache(embedder semantic.Embedder) *responsecache.ResponseCache {
	exact := responsecache.NewExactCache(responsecache.NewLRUStore(askCacheCapacity), askCacheTTL, false)
	var sem *responsecache.SemanticCache
	if embedder != nil {
		if sc, err := responsecache.NewSemanticCache(embedder, askSemanticThreshold, askSemanticTTL, askSemanticMaxPerScope); err != nil {
			slog.Error("response cache semantic layer disabled: construction failed; using exact-only", "err", err)
		} else {
			sem = sc
		}
	}
	return responsecache.New(exact, sem)
}

// guardrailsBlockFromEnv reports whether the named guardrail block toggle env
// var is set to a truthy value (per strconv.ParseBool). Unset, empty, or
// unparsable => false (off): the operator-fixed default posture is annotate +
// log, never block.
func guardrailsBlockFromEnv(name string) bool {
	return guardrailsBlockFromEnvDefault(name, false)
}

// guardrailsBlockFromEnvDefault is guardrailsBlockFromEnv with a caller-supplied
// default for the unset/empty/unparsable case, so a toggle can default ON
// (opt-out) instead of OFF (opt-in). An explicit truthy/falsey value always wins;
// an unparsable value falls back to def with a warning. Used to make
// AISOTROPY_GUARDRAILS_BLOCK_INJECTION default ON while keeping every default
// operator-overridable.
func guardrailsBlockFromEnvDefault(name string, def bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("invalid guardrail block toggle; using default", "var", name, "value", v, "default", def)
		return def
	}
	return on
}

// visionModelFromEnv reads AISOTROPY_VISION_MODEL: the gateway target NAME an
// image-bearing request is routed to via the ImageRule (buildModelGateway). It
// must name a client already registered in the gateway (in this runtime:
// gwPrimary or gwCheap); an unregistered name still routes safely to the default
// at Route time. Empty/unset (the default) means image requests route to the
// primary — the ImageRule stays wired but its target is the default target, so
// routing is unchanged from before this signal existed.
func visionModelFromEnv() string {
	return strings.TrimSpace(os.Getenv("AISOTROPY_VISION_MODEL"))
}

// costBudgetFromEnv parses AISOTROPY_COST_BUDGET_USD (a float, USD) for the
// gateway's cost-budget shaping rule. Falls back to 0 (rule disabled) when
// unset, unparsable, or non-positive, so the seam stays a pass-through unless a
// real budget is configured.
func costBudgetFromEnv() float64 {
	if v := os.Getenv("AISOTROPY_COST_BUDGET_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
		slog.Warn("invalid AISOTROPY_COST_BUDGET_USD; cost-budget shaping disabled", "value", v)
	}
	return 0
}

// newWorkingMemory opens on-disk BadgerDB-backed working memory when
// AISOTROPY_DATA_DIR is set, or an in-memory instance otherwise. The summary
// buffer is condensed by summarizer when non-nil (the consumer's
// WithSummarizer), else by the built-in memory.LLMSummarizer backed by the chat
// client — so a nil summarizer reproduces the pre-Wave-2 behavior exactly.
func newWorkingMemory(client llm.Client, summarizer memory.Summarizer) (*memory.WorkingMemory, error) {
	if summarizer == nil {
		summarizer = memory.LLMSummarizer{Client: client}
	}
	dataDir := os.Getenv("AISOTROPY_DATA_DIR")
	if dataDir == "" {
		// In-memory default: no disk footprint, so at-rest encryption is moot. If
		// a key was supplied anyway, warn — the operator most likely also intended
		// on-disk persistence (AISOTROPY_DATA_DIR).
		if dataEncryptionKeySet() {
			slog.Warn(dataEncryptionKeyEnv + " is set but AISOTROPY_DATA_DIR is not: working memory is in-memory (no disk), so at-rest encryption does not apply — set AISOTROPY_DATA_DIR to persist an encrypted store")
		}
		return memory.OpenInMemory(0, summarizer)
	}
	// On-disk working memory (Gap 5): when AISOTROPY_DATA_ENCRYPTION_KEY is set,
	// open BadgerDB with AES-at-rest encryption; otherwise behave exactly as
	// before (documented opt-in). A malformed key fails the boot with a clear
	// error rather than silently falling back to plaintext.
	key, encrypted, err := parseDataEncryptionKey()
	if err != nil {
		return nil, err
	}
	if encrypted {
		slog.Info("working memory at-rest encryption enabled (BadgerDB AES)", "key_bits", len(key)*8)
		return memory.OpenEncrypted(dataDir, 0, summarizer, key)
	}
	slog.Warn("working memory is on-disk but NOT app-encrypted at rest: set " + dataEncryptionKeyEnv + " to enable BadgerDB AES, or rely on operator disk/volume encryption (LUKS/dm-crypt/cloud-EBS) as the compensating control (see ADR-0044). The facts SQLite tier is disk-encryption-only regardless (ADR-0045).")
	return memory.Open(dataDir, 0, summarizer)
}
