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
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight HTTP
// requests (mainly open SSE streams) to finish before giving up.
const shutdownTimeout = 10 * time.Second

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

	// OTel tracing for the streaming chat path (ADR-0010: the streaming
	// path calls llm.Client.StreamChat directly, so it gets its own
	// provider-agnostic WithOTel(Client) decorator rather than Gollem's
	// ext/otel, which only instruments the Agent[T] reflection path). Defaults
	// to a no-op provider (no collector required); set
	// OTEL_EXPORTER_OTLP_ENDPOINT to export via OTLP/HTTP instead.
	shutdownTracing, tracer := llm.NewTracerProvider(context.Background())
	streamClient := llm.WithOTel(llmClient, tracer)

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
	api.Register(humaAPI)
	api.RegisterStream(humaAPI, streamClient, wm)

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
			_ = wm.Close()
			_ = shutdownTracing(context.Background())
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown did not complete cleanly", "err", err)
	}

	if err := wm.Close(); err != nil {
		slog.Error("working memory close failed", "err", err)
	}

	if err := shutdownTracing(shutdownCtx); err != nil {
		slog.Error("otel tracer shutdown failed", "err", err)
	}

	slog.Info("aisotropyd stopped")
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
