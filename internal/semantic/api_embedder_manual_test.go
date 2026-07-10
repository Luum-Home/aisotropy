//go:build manual

// Live validation for APIEmbedder (A.2 follow-up): proves the OpenAI-
// compatible /embeddings wire protocol (api_embedder.go) actually works
// against a REAL endpoint — OpenAI, OpenRouter, or a local server (Ollama,
// text-embeddings-inference, llama.cpp's server). Behind the `manual` build
// tag, so it is EXCLUDED from `go test ./...` and CI (it needs real
// credentials/network and, depending on the provider, spends real money).
//
// Run against OpenAI:
//
//	AISOTROPY_EMBED_URL=https://api.openai.com/v1 \
//	AISOTROPY_EMBED_API_KEY=sk-... \
//	AISOTROPY_EMBED_MODEL=text-embedding-3-small \
//	AISOTROPY_EMBED_DIM=1536 \
//	go test -tags manual -run TestAPIEmbedderLive -v ./internal/semantic
//
// Run against a local Ollama server (no API key needed):
//
//	AISOTROPY_EMBED_URL=http://localhost:11434/v1 \
//	AISOTROPY_EMBED_MODEL=nomic-embed-text \
//	AISOTROPY_EMBED_DIM=768 \
//	go test -tags manual -run TestAPIEmbedderLive -v ./internal/semantic
//
// Without AISOTROPY_EMBED_URL set, the test is a documented no-op (t.Skip),
// so it never fails a bare `go test -tags manual ./...` run in an
// environment with no embedding server configured.
package semantic

import (
	"context"
	"os"
	"testing"
)

// TestAPIEmbedderLive builds an APIEmbedder from env (EmbedderFromEnv, the
// same precedence aisotropy/knowledge.go's ensureSemantic uses) and embeds
// two short strings against the real, configured endpoint, asserting the
// response shape a real provider must satisfy: one vector per input, every
// vector non-zero, every vector's width matching the configured dimension.
func TestAPIEmbedderLive(t *testing.T) {
	if os.Getenv("AISOTROPY_EMBED_URL") == "" {
		t.Skip("AISOTROPY_EMBED_URL not set; point it at a real OpenAI-compatible " +
			"/embeddings endpoint (OpenAI, OpenRouter, or a local Ollama/TEI/llama.cpp " +
			"server) to run this live validation — see the file doc for exact env vars")
	}

	emb := EmbedderFromEnv()
	apiEmb, ok := emb.(*APIEmbedder)
	if !ok {
		// EmbedderFromEnv degrades to HashEmbedder on a config error (see
		// embedder_env.go) instead of panicking — catch that degrade here so
		// a misconfigured AISOTROPY_EMBED_DIM/URL fails loudly in this test
		// instead of silently validating the wrong embedder.
		t.Fatalf("EmbedderFromEnv did not build a real APIEmbedder (got %T); check AISOTROPY_EMBED_URL/AISOTROPY_EMBED_DIM for a config error logged above", emb)
	}

	ctx := context.Background()
	texts := []string{
		"the aisotropy onboarding handbook covers memory tiers",
		"frequently asked questions about billing and quotas",
	}

	vecs, err := apiEmb.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors, want %d (one per input)", len(vecs), len(texts))
	}

	wantDim := apiEmb.Dimension()
	for i, v := range vecs {
		if len(v) != wantDim {
			t.Errorf("vector %d has dimension %d, want the configured dimension %d", i, len(v), wantDim)
		}
		if allZero(v) {
			t.Errorf("vector %d is all-zero; a real embedding model should never return a zero vector for non-empty text", i)
		}
	}
}

// allZero reports whether every component of v is exactly 0.
func allZero(v []float32) bool {
	for _, f := range v {
		if f != 0 {
			return false
		}
	}
	return true
}
