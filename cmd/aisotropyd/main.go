// Command aisotropyd is the Aisotropy agent runtime binary.
//
// Phase 0 (walking skeleton — contracts): boots Huma on top of chi, registers
// the chat endpoint (stub), and serves /openapi.json + /docs. Phase 1 adds
// the SSE streaming endpoint backed by a real LLM (or the canned stub if
// ANTHROPIC_API_KEY is not set). Compiles into a single native binary (see
// docs/architecture/overview.md).
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

func main() {
	addr := os.Getenv("AISOTROPY_ADDR")
	if addr == "" {
		addr = ":8080"
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
	api.Register(humaAPI)

	llmClient := llm.FromEnv()
	api.RegisterStream(humaAPI, llmClient)

	slog.Info("aisotropyd starting", "addr", addr, "llm", llmClient.Name(), "openapi", "/openapi.json", "docs", "/docs")
	if err := http.ListenAndServe(addr, router); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
