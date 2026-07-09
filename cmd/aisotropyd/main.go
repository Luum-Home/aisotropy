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
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/consolidation"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/sleepcycle"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight HTTP
// requests (mainly open SSE streams) to finish before giving up.
const shutdownTimeout = 10 * time.Second

// outboxPollInterval is how often the consolidation worker checks for pending
// outbox events when the queue is empty (it drains back-to-back when it isn't).
const outboxPollInterval = 1 * time.Second

// sleepCycleInterval is how often the offline DataFusion consolidation runs when
// a worker binary is configured (Phase 6). Batch/offline, so a coarse cadence.
const sleepCycleInterval = 1 * time.Hour

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

	// Facts memory (internal/facts, Phase 3 "Facts memory" tier): SQLite+FTS5
	// by default (embedded, zero-config), or rqlite when RQLITE_URL is set
	// (ADR-0004). HeuristicExtractor is the deterministic, cheap Phase 3
	// extractor — richer LLM/DataFusion-driven distillation is Phase 6.
	factsStore, err := facts.OpenFromEnv()
	if err != nil {
		slog.Error("failed to initialize facts memory", "err", err)
		os.Exit(1)
	}
	factsExtractor := facts.HeuristicExtractor{}

	// Semantic memory (internal/semantic, Phase 4): in-memory vector store by
	// default, or Qdrant when QDRANT_HOST is set (ADR-0001). The embedder is
	// the deterministic HashEmbedder placeholder — a real embedding model is a
	// drop-in behind the Embedder interface.
	embedder := semantic.NewHashEmbedder(0)
	semanticStore, err := semantic.OpenFromEnv(context.Background(), embedder.Dimension())
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
	api.Register(humaAPI, protectedClient, wm, factsStore, rateLimiter)
	api.RegisterStream(humaAPI, streamClient, wm, factsStore, rateLimiter)

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
