package semantic

import (
	"log/slog"
	"os"
	"strconv"
)

// EmbedderFromEnv selects the semantic-tier Embedder from the environment,
// mirroring llm.FromEnv's provider-selection pattern
// (internal/llm/registry.go): a real backend when configured via env, a
// credential-less deterministic fallback otherwise.
//
// When AISOTROPY_EMBED_URL is set, it builds an APIEmbedder that speaks the
// OpenAI-compatible POST {base}/embeddings protocol — covering OpenAI,
// OpenRouter, or a fully local embedding server (Ollama, text-embeddings-
// inference, llama.cpp's server), the same BYO-endpoint sovereignty story as
// llm.OpenAICompatClient:
//
//   - AISOTROPY_EMBED_URL: the API base URL, e.g. "https://api.openai.com/v1"
//     or "http://localhost:11434/v1" for a local Ollama server. Required to
//     opt into APIEmbedder at all; unset means HashEmbedder.
//   - AISOTROPY_EMBED_MODEL: the embedding model id. Defaults to
//     defaultAPIEmbedModel when unset.
//   - AISOTROPY_EMBED_API_KEY: bearer token. Optional — most local servers
//     need none. Never logged.
//   - AISOTROPY_EMBED_DIM: the embedding vector width, as a positive integer.
//     This MUST match the semantic Store's own configured dimension, or
//     Upsert/Search fail with ErrDimensionMismatch. Most embedding APIs do
//     not report their dimension in the response, so it cannot be inferred;
//     set it explicitly for whichever model AISOTROPY_EMBED_MODEL names.
//     Unset or non-positive falls back to defaultEmbedDim with a warning
//     logged (never blocking — see the "falls back" note below).
//
// With AISOTROPY_EMBED_URL unset (the default), it returns NewHashEmbedder(0)
// — the deterministic, credential-less placeholder that wires the ingestion/
// retrieval path without a real backend (see embed.go's HashEmbedder doc).
func EmbedderFromEnv() Embedder {
	baseURL := os.Getenv("AISOTROPY_EMBED_URL")
	if baseURL == "" {
		return NewHashEmbedder(0)
	}

	dim := embedDimFromEnv()
	emb, err := NewAPIEmbedder(APIEmbedderConfig{
		BaseURL:   baseURL,
		Model:     os.Getenv("AISOTROPY_EMBED_MODEL"),
		APIKey:    os.Getenv("AISOTROPY_EMBED_API_KEY"),
		Dimension: dim,
	})
	if err != nil {
		// Only reachable if baseURL/dim validation above ever drifts out of
		// sync with NewAPIEmbedder's own checks; degrade to the safe
		// placeholder rather than let a misconfigured env panic the caller.
		slog.Warn("semantic: failed to build API embedder from env, falling back to HashEmbedder", "error", err)
		return NewHashEmbedder(0)
	}
	return emb
}

// embedDimFromEnv parses AISOTROPY_EMBED_DIM as a positive integer, falling
// back to defaultEmbedDim (with a warning) when unset or invalid — see
// EmbedderFromEnv's AISOTROPY_EMBED_DIM doc for why this cannot be inferred
// from the provider's response instead.
func embedDimFromEnv() int {
	raw := os.Getenv("AISOTROPY_EMBED_DIM")
	if raw == "" {
		slog.Warn("AISOTROPY_EMBED_URL set without AISOTROPY_EMBED_DIM, falling back to default dimension",
			"default_dim", defaultEmbedDim)
		return defaultEmbedDim
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("AISOTROPY_EMBED_DIM invalid, falling back to default dimension",
			"value", raw, "default_dim", defaultEmbedDim)
		return defaultEmbedDim
	}
	return n
}
