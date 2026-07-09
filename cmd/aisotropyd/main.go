// Command aisotropyd is the Aisotropy agent runtime binary.
//
// Phase 0 (walking skeleton — contracts): boots Huma on top of chi, registers
// the chat endpoint (stub), and serves /openapi.json + /docs. Phase 1 adds
// the SSE streaming endpoint backed by a real LLM (or the canned stub if
// ANTHROPIC_API_KEY is not set). Phase 2 follow-up "C" wires the BadgerDB
// working-memory tier (internal/memory) into the streaming path and adds
// graceful shutdown so in-flight background summarization can flush before
// the process exits. Compiles into a single native binary (see
// docs/architecture/overview.md).
package main

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
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/api"
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

// shutdownTimeout bounds how long graceful shutdown waits for in-flight HTTP
// requests (mainly open SSE streams) to finish before giving up.
const shutdownTimeout = 10 * time.Second

// outboxPollInterval is how often the consolidation worker checks for pending
// outbox events when the queue is empty (it drains back-to-back when it isn't).
const outboxPollInterval = 1 * time.Second

// agentMaxSteps bounds the tool-use loop on the agent endpoint.
const agentMaxSteps = 3

// primitiveFrictionThreshold is the accrued friction at which the sleep cycle
// rewrites a user's primitive.
const primitiveFrictionThreshold = 3.0

// sleepCycleInterval is how often the offline DataFusion consolidation runs when
// a worker binary is configured (Phase 6). Batch/offline, so a coarse cadence.
const sleepCycleInterval = 1 * time.Hour

// readinessProbeTimeout bounds each dependency probe on the GET /health
// readiness endpoint, so one hung backend cannot make readiness itself hang.
const readinessProbeTimeout = 2 * time.Second

// healthProbeTenant is the reserved tenant/user identity the readiness probes
// scope their read-only checks to, so a probe never touches real tenant data.
// The underscore prefix marks it internal; it contains no reserved key char.
const healthProbeTenant = "_health"

// healthProbeVector returns a cheap, valid unit query vector of the embedder's
// dimension for the semantic readiness probe (a zero vector can be rejected as
// zero-norm by some vector backends).
func healthProbeVector(dim int) []float32 {
	v := make([]float32, dim)
	if dim > 0 {
		v[0] = 1
	}
	return v
}

// Circuit breaker (Phase 7): after this many consecutive LLM start failures the
// breaker opens and calls fail fast until the cooldown elapses.
const (
	circuitFailureThreshold = 5
	circuitCooldown         = 30 * time.Second
)

func main() {
	addr := os.Getenv("AISOTROPY_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	llmClient := llm.FromEnv()

	// Working memory (internal/memory, Phase 2 "Working memory" tier): an
	// on-disk BadgerDB instance when AISOTROPY_DATA_DIR is set, otherwise an
	// in-memory instance (no persistence across restarts) so the binary
	// still runs with zero configuration. The summarizer is backed by the
	// same LLM client used for chat, so the condensed summary buffer is a
	// real condensation, not a heuristic placeholder.
	wm, err := newWorkingMemory(llmClient)
	if err != nil {
		slog.Error("failed to initialize working memory", "err", err)
		os.Exit(1)
	}

	// Physical multi-tenancy (ADR-0007 hybrid/tiered): tenants listed in
	// AISOTROPY_DEDICATED_TENANTS get their own physical facts/semantic resource
	// (a dedicated SQLite file / Qdrant collection), provisioned by the routing
	// layer; everyone else shares the pool. Empty (the default) = no routing.
	dedicatedTenants := splitCSV(os.Getenv("AISOTROPY_DEDICATED_TENANTS"))
	tierRouter := routing.NewRouter(routing.NewStaticPolicy(dedicatedTenants...))
	if len(dedicatedTenants) > 0 {
		slog.Info("dedicated-tenant routing enabled", "tenants", dedicatedTenants)
	}

	// Facts memory (internal/facts, Phase 3): SQLite+FTS5 by default, rqlite when
	// RQLITE_URL is set (ADR-0004). With dedicated tenants on the SQLite backend
	// each gets its own .db (facts.RoutingStore); rqlite dedicated tenants would
	// need a per-tenant cluster (infra), so routing there falls back to shared.
	factsStore, err := openFactsStore(tierRouter, dedicatedTenants)
	if err != nil {
		slog.Error("failed to initialize facts memory", "err", err)
		os.Exit(1)
	}
	factsExtractor := facts.HeuristicExtractor{}

	// Semantic memory (internal/semantic, Phase 4): in-memory by default, Qdrant
	// when QDRANT_HOST is set (ADR-0001). With dedicated tenants each gets its
	// own collection/index (semantic.RoutingStore). The embedder is the
	// deterministic HashEmbedder placeholder — a real model is a drop-in.
	embedder := semantic.NewHashEmbedder(0)
	semanticStore, err := openSemanticStore(context.Background(), tierRouter, dedicatedTenants, embedder.Dimension())
	if err != nil {
		slog.Error("failed to initialize semantic memory", "err", err)
		os.Exit(1)
	}

	// Consolidation sink (internal/consolidation): Phase 2 of the Transactional
	// Outbox. The async worker distils each drained event into the facts +
	// semantic tiers, idempotently (facts UPSERT, semantic UUIDv5). Wiring the
	// sink here — above the tiers — keeps the memory package free of
	// facts/semantic imports (see docs/architecture/consistency.md).
	sink := consolidation.Sink{
		Facts:     factsStore,
		Extractor: factsExtractor,
		Semantic:  semanticStore,
		Embedder:  embedder,
	}

	// Cold-boot reconciliation: drain any events orphaned by a previous
	// shutdown before serving, then run the worker for steady state. Both use
	// the same drain path (no special recovery mode).
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	if n, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
		slog.Error("cold-boot outbox reconciliation failed", "err", err)
	} else if n > 0 {
		slog.Info("cold-boot outbox reconciliation drained orphaned events", "count", n)
	}
	go wm.RunOutboxWorker(workerCtx, sink, outboxPollInterval)

	// OTel tracing for the streaming chat path (ADR-0010: the streaming
	// path calls llm.Client.StreamChat directly, so it gets its own
	// provider-agnostic WithOTel(Client) decorator rather than Gollem's
	// ext/otel, which only instruments the Agent[T] reflection path). Defaults
	// to a no-op provider (no collector required); set
	// OTEL_EXPORTER_OTLP_ENDPOINT to export via OTLP/HTTP instead.
	shutdownTracing, tracer := llm.NewTracerProvider(context.Background())
	// Circuit breaker (Phase 7): protect the runtime from a failing provider —
	// repeated StreamChat failures open the breaker and calls fail fast until a
	// cooldown elapses. Shared by both the streaming and reflection paths.
	protectedClient := llm.WithCircuitBreaker(llmClient, circuitFailureThreshold, circuitCooldown)
	streamClient := llm.WithOTel(protectedClient, tracer)

	// Optional cheap-model client for quota-pressure degradation (Phase 7): when
	// AISOTROPY_CHEAP_MODEL is set, a near-quota tenant is routed to it instead
	// of a hard 429. Wrapped with the same breaker + OTel as the primary; nil
	// (no degradation) when unconfigured.
	var cheapStreamClient llm.Client
	if raw := llm.CheapClientFromEnv(); raw != nil {
		cheapStreamClient = llm.WithOTel(llm.WithCircuitBreaker(raw, circuitFailureThreshold, circuitCooldown), tracer)
		slog.Info("cheap model configured for quota-pressure degradation", "model", raw.Name())
	}

	// Sleep cycle (Phase 6): when the DataFusion worker binary is available
	// (AISOTROPY_SLEEPCYCLE_BIN), run periodic offline consolidation — export
	// facts, deduplicate in the Rust worker (ADR-0005), and delete the dropped
	// duplicates from the live store. It is off the request path, so a failure
	// is logged, never fatal, and the worker's own panic is isolated as a
	// non-zero exit retried with backoff (internal/sleepcycle).
	if bin := os.Getenv("AISOTROPY_SLEEPCYCLE_BIN"); bin != "" {
		coordinator := sleepcycle.Coordinator{
			Store:  factsStore,
			Runner: sleepcycle.Worker{BinaryPath: bin, Tracer: tracer},
		}
		go coordinator.RunScheduler(workerCtx, sleepCycleInterval, func(s sleepcycle.Summary, err error) {
			if err != nil {
				slog.Error("sleep cycle failed", "err", err)
				return
			}
			if s.Dropped > 0 {
				slog.Info("sleep cycle consolidated facts", "rows_in", s.RowsIn, "dropped", s.Dropped)
			}
		})
		slog.Info("sleep-cycle worker scheduled", "bin", bin, "interval", sleepCycleInterval)
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)

	// Healthcheck outside the OpenAPI contract (liveness).
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// The Go struct is the single source of truth: Huma derives the OpenAPI
	// 3.1 schema and validates input from the same types (docs/vision/thesis.md).
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy", "0.1.0"))
	// Register (the non-streaming endpoint) uses the plain llmClient, not the
	// OTel-decorated streamClient: per ADR-0010's "two LLM call paths, two
	// instrumentation strategies", the reflection path's own instrumentation
	// is Gollem's ext/otel (not yet adopted — tracked as debt in the ADR),
	// not the streaming path's WithOTel(Client) decorator.
	// Per-tenant rate limiting (Phase 7 / constitutional gate): a token bucket
	// in the working-memory BadgerDB, checked before any LLM call so a tenant
	// over its limit is rejected (429) before spending tokens.
	rateLimiter := wm.NewLimiter(envFloat("AISOTROPY_RATE_PER_SEC", 10), envFloat("AISOTROPY_BURST", 30))

	// Scope-gated tool registry (Phase 7, behavior axis): a tool is injected
	// into the LLM payload only when the request's scopes permit it — otherwise
	// it does not exist for the model (docs/architecture/granular-control.md).
	toolRegistry := tools.NewRegistry(
		tools.Tool{Name: "search_docs", Description: "search the tenant knowledge base"},
		tools.Tool{Name: "read_financials", Description: "read financial records", RequiredScope: "read:financial-data"},
		tools.Tool{Name: "write_tasks", Description: "create and update tasks", RequiredScope: "write:tasks"},
	)

	// Per-tenant cost meter (Phase 7 observability): aggregates the real token
	// usage recorded on each streamed turn. Exposed at GET /costs (outside the
	// OpenAPI contract, like /healthz) as a per-tenant cost snapshot.
	costMeter := costmeter.New()
	router.Get("/costs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(costMeter.Snapshot())
	})

	// Readiness (distinct from /healthz liveness): exercise the networked memory
	// tiers with a cheap, read-only, health-tenant-scoped probe so a load
	// balancer can tell "process up" apart from "dependencies reachable". No
	// probe spends LLM tokens — actively calling the model on every poll would
	// violate the "reject/degrade before spending tokens" gate — so LLM health
	// is not probed here (see internal/health). Responds 200 ready / 503 degraded.
	router.Get("/health", health.New(readinessProbeTimeout,
		health.Probe{Name: "facts", Check: func(ctx context.Context) error {
			sc, err := facts.NewScope(healthProbeTenant, healthProbeTenant)
			if err != nil {
				return err
			}
			_, err = factsStore.Search(ctx, sc, "health", 1)
			return err
		}},
		health.Probe{Name: "semantic", Check: func(ctx context.Context) error {
			sc := semantic.Scope{TenantID: healthProbeTenant, UserID: healthProbeTenant}
			_, err := semanticStore.Search(ctx, sc, healthProbeVector(embedder.Dimension()), 1)
			return err
		}},
	).Handler())

	api.Register(humaAPI, protectedClient, wm, factsStore, rateLimiter, toolRegistry)
	api.RegisterStream(humaAPI, streamClient, wm, factsStore, rateLimiter, toolRegistry, costMeter, cheapStreamClient, semanticStore, embedder)

	// Per-user primitives (Phase 6 behavioral memory): failed tool invocations
	// on the agent endpoint accrue friction here; the sleep cycle rewrites a
	// primitive once its friction crosses the threshold.
	primStore := primitives.NewStore()
	frictionDetector := primitives.NewFrictionDetector(primStore)
	go runPrimitiveConsolidation(workerCtx, primStore)

	// Agentic tool-use endpoint (Phase 7 behavior axis): the structured-output
	// loop over the scope-permitted tools, with a friction-recording executor.
	api.RegisterAgent(humaAPI, protectedClient, toolRegistry, frictionDetector, agentMaxSteps)

	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	// Graceful shutdown: on SIGINT/SIGTERM, stop accepting new connections,
	// let in-flight requests (including open SSE streams) drain within
	// shutdownTimeout, and only then close working memory so any in-flight
	// background condensation goroutine gets to flush (see
	// memory.WorkingMemory.Close).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("aisotropyd starting", "addr", addr, "llm", llmClient.Name(), "openapi", "/openapi.json", "docs", "/docs")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections", "timeout", shutdownTimeout)
	case err := <-serverErrCh:
		if err != nil {
			slog.Error("server stopped", "err", err)
			stopWorker()
			_ = wm.Close()
			_ = factsStore.Close()
			_ = semanticStore.Close()
			_ = shutdownTracing(context.Background())
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown did not complete cleanly", "err", err)
	}

	// Stop the consolidation worker before closing the stores it writes to, so
	// no drain is in flight against a closing BadgerDB / facts / semantic store.
	stopWorker()

	if err := wm.Close(); err != nil {
		slog.Error("working memory close failed", "err", err)
	}

	if err := factsStore.Close(); err != nil {
		slog.Error("facts memory close failed", "err", err)
	}

	if err := semanticStore.Close(); err != nil {
		slog.Error("semantic memory close failed", "err", err)
	}

	if err := shutdownTracing(shutdownCtx); err != nil {
		slog.Error("otel tracer shutdown failed", "err", err)
	}

	slog.Info("aisotropyd stopped")
}

// runPrimitiveConsolidation periodically rewrites per-user primitives whose
// accrued friction crossed the threshold (Phase 6 sleep cycle, behavioral
// memory). It is a no-op until real friction accrues (the demo tool executor
// never fails), but the mechanism runs so a deployment with failing tools
// improves them over time.
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
// on the SQLite backend (each dedicated tenant → its own .db file / in-memory
// DB). rqlite dedicated tenants would need a per-tenant cluster (infra), so
// routing is not applied there.
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

// envFloat reads a positive float from the named env var, or returns def.
func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

// newWorkingMemory opens the on-disk BadgerDB-backed working memory when
// AISOTROPY_DATA_DIR is set, or an in-memory instance otherwise. It always
// uses memory.LLMSummarizer backed by client so the condensed summary buffer
// is produced by a real LLM call rather than the deterministic
// NoopSummarizer used in tests/benchmarks.
func newWorkingMemory(client llm.Client) (*memory.WorkingMemory, error) {
	summarizer := memory.LLMSummarizer{Client: client}

	dataDir := os.Getenv("AISOTROPY_DATA_DIR")
	if dataDir == "" {
		return memory.OpenInMemory(0, summarizer)
	}
	return memory.Open(dataDir, 0, summarizer)
}
