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
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/health"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/primitives"
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
	// bitrateInterval is how often the offline bits-per-token-per-tenant
	// measurement runs (Bet 6) when a scorer binary is configured.
	bitrateInterval = 1 * time.Hour
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
)

// Default rate-limit values used when WithRateLimit is not supplied. They mirror
// the AISOTROPY_RATE_PER_SEC / AISOTROPY_BURST fallbacks in the original binary.
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
	inPricePer1M  float64
	outPricePer1M float64
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

	// Semantic memory (Phase 4): deterministic HashEmbedder + in-memory/Qdrant by
	// env. Resolve the embedder first, since the store dimension derives from it.
	embedder := semantic.NewHashEmbedder(0)
	semanticStore, err := openSemanticStore(ctx, tierRouter, dedicatedTenants, embedder.Dimension())
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

	// Consolidation sink (Phase 5 outbox): distils each drained event into the
	// facts + semantic tiers idempotently.
	sink := consolidation.Sink{
		Facts:     factsStore,
		Extractor: factsExtractor,
		Semantic:  semanticStore,
		Embedder:  embedder,
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

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)

	// Liveness, outside the OpenAPI contract.
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy", "0.1.0"))

	// Per-tenant rate limiting (Phase 7 / constitutional gate).
	rateLimiter := wm.NewLimiter(a.ratePerSec, a.burst)

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

	// Per-tenant cost meter, exposed at GET /costs.
	costMeter := costmeter.New()
	router.Get("/costs", func(w http.ResponseWriter, _ *http.Request) {
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
		router.Get("/bitrate", func(w http.ResponseWriter, r *http.Request) {
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
			sc := semantic.Scope{TenantID: healthProbeTenant, UserID: healthProbeTenant}
			_, serr := semanticStore.Search(ctx, sc, healthProbeVector(embedder.Dimension()), 1)
			return serr
		}},
	).Handler())

	api.Register(humaAPI, protectedClient, wm, factsStore, rateLimiter, toolRegistry, persona)
	api.RegisterStream(humaAPI, streamClient, wm, factsStore, rateLimiter, toolRegistry, costMeter, cheapStreamClient, semanticStore, embedder, persona, a.inPricePer1M, a.outPricePer1M, bptSampler)

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
	api.RegisterAgent(humaAPI, protectedClient, toolRegistry, executor, typedExecutor, frictionDetector, tracerProvider, agentMaxSteps)

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
