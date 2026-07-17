package semantic

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
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
// Selection precedence (first match wins):
//
//  1. AISOTROPY_EMBED_BIN set -> the sovereign LOCAL embedder (Bet 5, ADR-0023):
//     a candle subprocess that keeps tenant text on-machine (zero network). This
//     is the thesis default and outranks the API path when both are configured.
//     AISOTROPY_EMBED_MODE selects the form: "daemon" (default — one warm
//     process, ~18.8× faster per cmd/embed-bench) or "subprocess" (a fresh
//     process per call, panic-isolated). Requires AISOTROPY_EMBED_MODEL_DIR and
//     AISOTROPY_EMBED_DIM; a config error logs a warning and falls through.
//  2. AISOTROPY_EMBED_URL set -> APIEmbedder (BYO /embeddings endpoint, below).
//  3. neither -> NewHashEmbedder(0), the deterministic credential-less
//     placeholder that wires the ingestion/retrieval path without a real backend
//     (see embed.go's HashEmbedder doc).
func EmbedderFromEnv() Embedder {
	if bin := os.Getenv("AISOTROPY_EMBED_BIN"); bin != "" {
		if emb := localEmbedderFromEnv(bin); emb != nil {
			return emb
		}
		// Config error already logged; fall through to the API/Hash path rather
		// than fail closed on a misconfigured local embedder.
	}

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

// localEmbedderFromEnv builds the sovereign local embedder (ADR-0023) from env,
// or returns nil (with a warning) on a config error so EmbedderFromEnv can fall
// through to the API/Hash path. AISOTROPY_EMBED_MODE picks the form: "" or
// "daemon" -> DaemonEmbedder (one warm process); "subprocess" -> LocalEmbedder
// (a fresh process per call). Dimension comes from AISOTROPY_EMBED_DIM and MUST
// match both the model's hidden size and the semantic Store's dimension.
func localEmbedderFromEnv(bin string) Embedder {
	modelDir := os.Getenv("AISOTROPY_EMBED_MODEL_DIR")
	if modelDir == "" {
		slog.Warn("AISOTROPY_EMBED_BIN set without AISOTROPY_EMBED_MODEL_DIR; ignoring local embedder",
			"bin", bin)
		return nil
	}
	dim := embedDimFromEnv()
	mode := os.Getenv("AISOTROPY_EMBED_MODE")
	e5 := e5PrefixesEnabled(modelDir)

	if mode == "subprocess" {
		emb, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: modelDir, Dimension: dim, E5Prefixes: e5})
		if err != nil {
			slog.Warn("semantic: failed to build local (subprocess) embedder from env, falling through", "error", err)
			return nil
		}
		slog.Info("semantic embedder selected", "type", "semantic.LocalEmbedder", "mode", "subprocess", "dimension", dim, "e5_prefixes", e5)
		return emb
	}

	emb, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: bin, ModelDir: modelDir, Dimension: dim, E5Prefixes: e5})
	if err != nil {
		slog.Warn("semantic: failed to build local (daemon) embedder from env, falling through", "error", err)
		return nil
	}
	slog.Info("semantic embedder selected", "type", "semantic.DaemonEmbedder", "mode", "daemon", "dimension", dim, "e5_prefixes", e5)
	return emb
}

// e5PrefixesEnabled decides whether the local embedder should apply e5
// query:/passage: instruction prefixes (ADR-0054). It is model-aware, not
// unconditional: an explicit AISOTROPY_EMBED_E5_PREFIXES ("1"/"true"/"0"/
// "false", any strconv.ParseBool form) always wins; when unset it autodetects
// the e5 family from the model dir path (the ADR-0049 default is
// intfloat/multilingual-e5-small, so an "e5" marker in the pinned model path is
// the signal). A non-e5 model — or an explicit "false" — disables prefixing, so
// those vectors are left unaffected by the query/passage convention.
func e5PrefixesEnabled(modelDir string) bool {
	if raw := os.Getenv("AISOTROPY_EMBED_E5_PREFIXES"); raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
		slog.Warn("AISOTROPY_EMBED_E5_PREFIXES invalid, autodetecting e5 prefixes from model dir",
			"value", raw, "model_dir", modelDir)
	}
	return isE5Model(modelDir)
}

// isE5Model reports whether path names an e5-family model, matched
// case-insensitively on a delimited "e5" TOKEN in the pinned model dir (e.g.
// ".../multilingual-e5-small", ".../e5-base-v2"). Detection tokenizes on
// non-alphanumeric boundaries and looks for the exact token "e5", so an
// unrelated name that merely ends in "e5" (e.g. "base5-experimental") does NOT
// false-match. It is deliberately conservative — only the e5 family uses the
// query:/passage: convention — and AISOTROPY_EMBED_E5_PREFIXES is the
// authoritative override when the path carries no recognizable token.
func isE5Model(path string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		return !isAlphaNum // split on every non-alphanumeric boundary
	})
	for _, tok := range tokens {
		if tok == "e5" {
			return true
		}
	}
	return false
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
