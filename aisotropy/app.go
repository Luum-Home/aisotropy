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

	"github.com/Luum-Home/aisotropy/internal/api"
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
	llmClient     llm.Client
	ratePerSec    float64
	burst         float64
	rateLimitSet  bool
	inPricePer1M  float64
	outPricePer1M float64

	// authenticator backs WithAuthenticator: the single fail-closed gate the
	// auth middleware invokes in front of every /v1 route. nil means no
	// authenticator was registered — Run then installs a default-deny gate
	// (Option C) that 401s every /v1 request. Use WithAuthenticator(NoAuth())
	// to explicitly run without a credential check.
	authenticator Authenticator

	// embedder / chunkSize / chunkOverlap back WithEmbedder / WithChunkSize
	// (ADR-0018, aisotropy/knowledge.go). chunkSize <= 0 means "unset": the
	// façade falls back to semantic.DefaultChunkOptions().
	embedder     semantic.Embedder
	chunkSize    int
	chunkOverlap int

	// semanticOnce/semanticStore/semanticErr memoize ensureSemantic's
	// lazily-built embedder+store pair, shared between Run and
	// IngestKnowledge (aisotropy/knowledge.go) so the two surfaces never
	// build two independently evolving semantic indices for the same App.
	semanticOnce  sync.Once
	semanticStore semantic.Store
	semanticErr   error

	// closeOnce/closeErr back Close (below): idempotent release of resources
	// this App memoized outside of Run's own lifecycle (currently: the lazily
	// built semantic store).
	closeOnce sync.Once
	closeErr  error
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
		ratePerSec: defaultRatePerSec,
		burst:      defaultBurst,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// WithAddr sets the HTTP listen address. Default: AISOTROPY_ADDR, or ":8080".
func WithAddr(addr string) Option {
	return func(a *App) { a.addr = addr }
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

// Run boots the full runtime and blocks until graceful shutdown. It internalizes
// exactly what cmd/aisotropyd/main.go did by hand: working memory, tier routing,
// facts + semantic stores, the consolidation sink + outbox worker (+ cold-boot
// reconciliation), OTel tracing, the LLM circuit breaker, cheap-model
// degradation, the sleep-cycle worker, per-tenant rate limiting, the cost meter,
// the /healthz + /health + /costs endpoints, the three /v1 chat routes, per-user
// primitive friction consolidation, and SIGINT/SIGTERM graceful shutdown that
// closes every store.
//
// ctx cancellation, SIGINT, or SIGTERM all trigger graceful shutdown. Run
// returns nil on a clean shutdown and a non-nil error only if the HTTP server
// itself fails to start/serve.
//
//nolint:gocognit,cyclop,funlen // large cohesive request handler; refactor tracked in docs/architecture/phases-3-7-as-built.md debt ledger
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := a.addr
	if addr == "" {
		addr = os.Getenv("AISOTROPY_ADDR")
	}
	if addr == "" {
		addr = ":8080"
	}

	llmClient := a.llmClient
	if llmClient == nil {
		llmClient = llm.FromEnv()
	}

	// Working memory: on-disk BadgerDB when AISOTROPY_DATA_DIR is set, else
	// in-memory. The summarizer is backed by the chat LLM so the condensed buffer
	// is a real condensation.
	wm, err := newWorkingMemory(llmClient)
	if err != nil {
		return err
	}

	// Physical multi-tenancy (ADR-0007): tenants in AISOTROPY_DEDICATED_TENANTS
	// get their own physical facts/semantic resource; everyone else shares the
	// pool. Empty (default) = no routing.
	dedicatedTenants := splitCSV(os.Getenv("AISOTROPY_DEDICATED_TENANTS"))
	tierRouter := routing.NewRouter(routing.NewStaticPolicy(dedicatedTenants...))
	if len(dedicatedTenants) > 0 {
		slog.Info("dedicated-tenant routing enabled", "tenants", dedicatedTenants)
	}

	// Facts memory (Phase 3): SQLite+FTS5 / rqlite by env (ADR-0007 routing).
	factsStore, err := openFactsStore(tierRouter, dedicatedTenants) //nolint:contextcheck // facts.OpenSQLite has no context parameter (internal/facts, out of scope for this cleanup)
	if err != nil {
		_ = wm.Close()
		return err
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
		return err
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
			return berr
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

	// Cold-boot reconciliation, then the steady-state worker.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	if n, rerr := wm.RecoverOutbox(ctx, sink); rerr != nil {
		slog.Error("cold-boot outbox reconciliation failed", "err", rerr)
	} else if n > 0 {
		slog.Info("cold-boot outbox reconciliation drained orphaned events", "count", n)
	}
	go wm.RunOutboxWorker(workerCtx, sink, outboxPollInterval) //nolint:contextcheck // workerCtx intentionally outlives ctx (which is cancelled on signal) so this worker keeps draining until stopWorker() during graceful shutdown

	// OTel tracing (ADR-0010, no-op by default), circuit breaker, and the OTel
	// decorator shared by the streaming and reflection paths.
	shutdownTracing, tracer, tracerProvider := llm.NewTracerProvider(context.Background()) //nolint:contextcheck // tracer lifetime must outlive ctx (cancelled on signal): shutdownTracing is invoked later, during graceful shutdown, with its own bounded context
	protectedClient := llm.WithCircuitBreaker(llmClient, circuitFailureThreshold, circuitCooldown)
	streamClient := llm.WithOTel(protectedClient, tracer)

	// Optional cheap-model client for quota-pressure degradation (Phase 7).
	var cheapStreamClient llm.Client
	if raw := llm.CheapClientFromEnv(); raw != nil {
		cheapStreamClient = llm.WithOTel(llm.WithCircuitBreaker(raw, circuitFailureThreshold, circuitCooldown), tracer)
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
		go coordinator.RunScheduler(workerCtx, sleepCycleInterval, func(s sleepcycle.Summary, serr error) { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			if serr != nil {
				slog.Error("sleep cycle failed", "err", serr)
				return
			}
			if s.Dropped > 0 {
				slog.Info("sleep cycle consolidated facts", "rows_in", s.RowsIn, "dropped", s.Dropped)
			}
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
		go pruner.RunTurnPruneScheduler(workerCtx, turnPruneInterval, func(perr error) { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			slog.Error("turn-vector pruning failed", "err", perr)
		})
		slog.Info("turn-vector pruning scheduled", "ttl", semanticTurnTTLFromEnv(), "max", semanticTurnMaxFromEnv(), "interval", turnPruneInterval)
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)

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

	// Scope-gated tool registry: the consumer's registered tools plus any
	// MCP-discovered tools.
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

	// Operator-endpoint gate (default-closed): /costs and /bitrate return
	// ALL-tenant aggregates, so they live behind a shared operator token
	// (AISOTROPY_OPS_TOKEN) instead of the per-tenant /v1 JWT gate. With the
	// token unset every request gets 503; with it set the request must carry a
	// matching "Authorization: Bearer <token>" (401 otherwise). Health/liveness/
	// openapi/docs stay open.
	opsGate := api.OpsAuthGate(api.OpsTokenFromEnv())

	// Per-tenant cost meter, exposed at GET /costs.
	costMeter := costmeter.New()
	router.With(opsGate).Get("/costs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(costMeter.Snapshot())
	})

	// Bitrate (Bet 6): when the store is enabled, schedule the offline measurement
	// and expose the per-tenant ops view. The coordinator hydrates each held-out
	// sample's context THROUGH the api hydrator seam (the exact request-path
	// hydration — anti-magic, ADR-0002), scores with/without memory in the scorer
	// subprocess, and persists the lift.
	if bitrateStore != nil {
		bin := os.Getenv("AISOTROPY_BITRATE_BIN")
		coordinator := bitrate.Coordinator{
			Sampler: bitrateStore,
			Hydrator: api.BitrateHydrator{
				Store:    factsStore,
				Semantic: semanticStore,
				Embedder: embedder,
				Persona:  persona,
			},
			Runner: bitrate.Worker{BinaryPath: bin, Tracer: tracer},
			Store:  bitrateStore,
		}
		go coordinator.RunScheduler(workerCtx, bitrateInterval, func(ms []bitrate.Measurement, merr error) { //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown
			if merr != nil {
				slog.Error("bitrate measurement failed", "err", merr)
				return
			}
			if len(ms) > 0 {
				slog.Info("bitrate measured memory lift", "samples", len(ms))
			}
		})
		router.With(opsGate).Get("/bitrate", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			summaries, serr := bitrateStore.AllSummaries(r.Context())
			if serr != nil {
				http.Error(w, `{"error":"bitrate summary failed"}`, http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(summaries)
		})
		slog.Info("bitrate scorer scheduled", "bin", bin, "interval", bitrateInterval)
	} else {
		slog.Info("bitrate scorer disabled", "reason", "AISOTROPY_BITRATE_BIN unset")
	}

	// Readiness: cheap, read-only, health-tenant-scoped probes of the networked
	// tiers. No probe spends LLM tokens.
	router.Get("/health", health.New(readinessProbeTimeout,
		health.Probe{Name: "facts", Check: func(ctx context.Context) error {
			sc, serr := facts.NewScope(healthProbeTenant, healthProbeTenant) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
			if serr != nil {
				return serr
			}
			_, serr = factsStore.Search(ctx, sc, "health", 1)
			return serr
		}},
		health.Probe{Name: "semantic", Check: func(ctx context.Context) error {
			sc, serr := semantic.NewScope(healthProbeTenant, healthProbeTenant) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
			if serr != nil {
				return serr
			}
			_, serr = semanticStore.Search(ctx, sc, healthProbeVector(embedder.Dimension()), 1)
			return serr
		}},
	).Handler())

	// Guardrails (redact + detect ALWAYS; blocking OPT-IN per operator toggles,
	// default off): one process-wide aggregator threaded into every chat handler.
	// The env toggles gate only whether a suspicious verdict rejects the
	// request/response — redaction and detection run regardless.
	blockOutput := guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT")

	// The output-inspection tier: the built-in heuristics by default, plus an
	// OPT-IN LLM-as-a-judge Safety check when AISOTROPY_GUARDRAILS_LLM_JUDGE is
	// truthy (and an LLM client exists). The judge attaches through the
	// api.EvalOutputCheck adapter over the guardrails.OutputCheck seam, so
	// internal/guardrails still never imports internal/eval. The judge is driven
	// by the RAW llmClient — deliberately NOT the circuit-broken chat client — so
	// a flaky judge cannot trip the chat breaker. Its error posture tracks
	// blockOutput (fail-open when annotating, fail-closed when hard-blocking); see
	// api.NewEvalOutputCheck. Default off: keyless/stub runtimes are byte-for-byte
	// unchanged.
	guardrailsG := guardrails.Default()
	if guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_LLM_JUDGE") && llmClient != nil {
		if judge, jerr := eval.NewLLMJudge(llmClient, eval.Safety, eval.SafetyRubric, 0); jerr != nil {
			slog.Error("guardrails LLM judge disabled: judge construction failed; using built-in output heuristics only", "err", jerr)
		} else if check, cerr := api.NewEvalOutputCheck(judge, guardrailsJudgeSafetyThreshold, blockOutput); cerr != nil {
			slog.Error("guardrails LLM judge disabled: adapter construction failed; using built-in output heuristics only", "err", cerr)
		} else {
			inspector := guardrails.NewOutputInspector(nil).WithChecks(check)
			guardrailsG = guardrails.New(guardrails.Config{Output: inspector})
			slog.Info("guardrails LLM safety judge attached to the output inspector",
				"criterion", eval.Safety.Name, "threshold", guardrailsJudgeSafetyThreshold, "fail_closed", blockOutput)
		}
	}

	// One process-wide aggregator threaded into every chat handler. The block
	// toggles gate only whether a suspicious verdict rejects the request/replaces
	// the response — redaction and detection run regardless.
	guard := &api.Guard{
		G:              guardrailsG,
		BlockInjection: guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_INJECTION"),
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
	modelGateway := buildModelGateway(streamClient, cheapStreamClient, costBudgetFromEnv(), visionModelFromEnv())

	api.Register(humaAPI, protectedClient, wm, factsStore, rateLimiter, toolRegistry, persona, costMeter, modelGateway, guard)
	api.RegisterStream(humaAPI, streamClient, wm, factsStore, rateLimiter, toolRegistry, costMeter, cheapStreamClient, semanticStore, embedder, persona, a.inPricePer1M, a.outPricePer1M, bptSampler, modelGateway, guard)
	// Knowledge ingestion (ADR-0018): the HTTP arm of the dual-surface
	// capability, sharing the exact same semantic store/embedder pair the
	// chat routes' RAG hydration reads from above.
	api.RegisterKnowledge(humaAPI, semanticStore, embedder, a.chunkOptions())
	// Knowledge deletion (ADR-0018 open q#3): removes a document's chunks by
	// id, regardless of chunk count.
	api.RegisterKnowledgeDelete(humaAPI, semanticStore)
	// Tenant-shared knowledge ingestion + deletion (ADR-0019): a corpus
	// ingested once at tenant scope, retrievable by every user of that
	// tenant via hydrateSystemPrompt's merged search.
	api.RegisterTenantKnowledge(humaAPI, semanticStore, embedder, a.chunkOptions())
	api.RegisterTenantKnowledgeDelete(humaAPI, semanticStore)

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
	askCache := buildResponseCache(embedder)
	api.RegisterAsk(humaAPI, protectedClient, askCache, semanticStore, embedder, persona, guard)

	// Per-user primitives (Phase 6 behavioral memory).
	primStore := primitives.NewStore()
	frictionDetector := primitives.NewFrictionDetector(primStore)
	go runPrimitiveConsolidation(workerCtx, primStore) //nolint:contextcheck // workerCtx intentionally outlives ctx (cancelled on signal) so background schedulers keep running until stopWorker() during graceful shutdown

	// Agentic tool-use endpoint (Phase 7): the injected executor drives real tool
	// logic, wrapped per-request in a friction recorder.
	// Typed-tool dispatcher (ADR-0016 lever #1): raw-JSON handlers registered via
	// WithTypedTool[T]. Preferred over the map executor for any tool it owns; nil
	// handlers => an executor that owns nothing, so behavior is unchanged.
	typedExecutor := api.NewTypedToolExecutor(typedHandlers)
	api.RegisterAgent(humaAPI, protectedClient, toolRegistry, executor, typedExecutor, frictionDetector, tracerProvider, agentMaxSteps, guard)

	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("aisotropyd starting", "addr", addr, "llm", llmClient.Name(), "openapi", "/openapi.json", "docs", "/docs")
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
			stopWorker()
			closeMCP()
			_ = wm.Close()
			_ = factsStore.Close()
			_ = semanticStore.Close()
			if bitrateStore != nil {
				_ = bitrateStore.Close()
			}
			_ = shutdownTracing(context.Background()) //nolint:contextcheck // ctx is already cancelled here (server failed to start after a shutdown signal); tracer shutdown deliberately uses a fresh context
			return serr
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if serr := server.Shutdown(shutdownCtx); serr != nil { //nolint:contextcheck // ctx is already cancelled at this point (draining triggered by ctx.Done()); shutdownCtx deliberately gives the drain its own bounded deadline
		slog.Error("server shutdown did not complete cleanly", "err", serr)
	}

	// Stop the consolidation worker before closing the stores it writes to.
	stopWorker()
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
		return semantic.OpenQdrant(ctx, semantic.QdrantConfig{
			Host:       os.Getenv("QDRANT_HOST"),
			Port:       port,
			APIKey:     os.Getenv("QDRANT_API_KEY"),
			UseTLS:     os.Getenv("QDRANT_USE_TLS") == "true",
			Collection: resource,
			Dim:        dim,
		})
	}
	if len(dedicated) == 0 {
		return factory(base)
	}
	return semantic.NewRoutingStore(router, base, dedicated, factory)
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
func buildModelGateway(primary, cheap llm.Client, budgetUSD float64, visionTarget string) *gateway.Gateway {
	// OverridePolicy FIRST so an explicit caller preference (ChatRequest.
	// ModelPreference -> gateway.Request.UISelector) wins over the cost-budget
	// rule. It is inert-safe: it abstains when UISelector is empty, so requests
	// with no preference route exactly as before. The streaming path's
	// quota-pressure DegradeAt still overrides even an honored preference (the
	// safety valve), because it is applied AFTER the gateway pick (see
	// RegisterStream). An unknown preference target resolves to the default at
	// Route time, so a client value can never drop a request.
	policies := []gateway.Policy{gateway.OverridePolicy{}}

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
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("invalid guardrail block toggle; treating as off", "var", name, "value", v)
		return false
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
// AISOTROPY_DATA_DIR is set, or an in-memory instance otherwise, always with the
// LLM-backed summarizer.
func newWorkingMemory(client llm.Client) (*memory.WorkingMemory, error) {
	summarizer := memory.LLMSummarizer{Client: client}
	dataDir := os.Getenv("AISOTROPY_DATA_DIR")
	if dataDir == "" {
		return memory.OpenInMemory(0, summarizer)
	}
	return memory.Open(dataDir, 0, summarizer)
}
