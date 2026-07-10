package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultAPIEmbedModel is the model id used when AISOTROPY_EMBED_MODEL is
// unset — OpenAI's cheapest current embeddings model. Any OpenAI-compatible
// backend (OpenRouter, a local Ollama/text-embeddings-inference/llama.cpp
// server) is free to ignore or override it via AISOTROPY_EMBED_MODEL.
const defaultAPIEmbedModel = "text-embedding-3-small"

// apiEmbedTimeout bounds one Embed call. Embedding batches are typically much
// smaller/cheaper than a chat completion, so this is shorter than
// OpenAICompatClient's 120s chat timeout (internal/llm/openai_compat.go).
const apiEmbedTimeout = 60 * time.Second

// APIEmbedder is a real, BYO Embedder that speaks the OpenAI-compatible
// POST {baseURL}/embeddings protocol — the same "raw net/http + JSON, zero
// vendor SDK" posture as llm.OpenAICompatClient
// (internal/llm/openai_compat.go), so it works unmodified against OpenAI,
// OpenRouter, or a fully local embedding server (Ollama, text-embeddings-
// inference, llama.cpp's server) that speaks the same wire protocol. This
// preserves both the "100% MIT/Apache, no bundled model" posture and the BYO
// sovereignty story: an operator can point Embed at their own infrastructure
// with no code change and no dependency on this repo trusting a third party.
//
// Unlike HashEmbedder (embed.go), APIEmbedder is genuinely semantic — quality
// depends entirely on the configured model, not on this package.
type APIEmbedder struct {
	baseURL string
	model   string
	apiKey  string
	dim     int
	http    *http.Client
}

// APIEmbedderConfig configures an APIEmbedder.
type APIEmbedderConfig struct {
	// BaseURL is the API root, e.g. "https://api.openai.com/v1",
	// "https://openrouter.ai/api/v1", or "http://localhost:11434/v1" for a
	// local Ollama server. Required. Embed POSTs to BaseURL + "/embeddings".
	BaseURL string
	// Model is the embedding model id sent in the request body. Defaults to
	// defaultAPIEmbedModel when empty.
	Model string
	// APIKey is sent as a Bearer token when non-empty. Optional: most local
	// servers require no authentication. Never logged.
	APIKey string
	// Dimension is the embedding vector width this model produces. Required
	// (must be positive): most embedding APIs return a fixed dimension per
	// model and do not report it in the response, so it must be configured
	// explicitly and must match the semantic Store's own configured
	// dimension, or Upsert/Search fail with ErrDimensionMismatch.
	Dimension int
}

// NewAPIEmbedder validates cfg and returns a ready APIEmbedder. BaseURL must
// be non-empty and Dimension must be positive; Model defaults to
// defaultAPIEmbedModel when empty.
func NewAPIEmbedder(cfg APIEmbedderConfig) (*APIEmbedder, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("semantic: api embedder requires a non-empty base URL")
	}
	if cfg.Dimension <= 0 {
		return nil, errors.New("semantic: api embedder requires a positive vector dimension")
	}
	model := cfg.Model
	if model == "" {
		model = defaultAPIEmbedModel
	}
	return &APIEmbedder{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   model,
		apiKey:  cfg.APIKey,
		dim:     cfg.Dimension,
		http:    &http.Client{Timeout: apiEmbedTimeout},
	}, nil
}

// Dimension reports the configured vector width.
func (e *APIEmbedder) Dimension() int { return e.dim }

// embedRequest is the OpenAI-compatible POST /embeddings request body.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the subset of the OpenAI-compatible /embeddings response
// shape Embed needs: one Data entry per input, each carrying the vector and
// its Index into the original input slice (providers are not guaranteed to
// echo Data back in request order, so Index is load-bearing, not decorative).
type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed POSTs texts to {baseURL}/embeddings and returns one vector per input,
// in input order. It honors ctx cancellation via http.NewRequestWithContext.
// A non-2xx response, a malformed response body, or a response whose vector
// count/index set does not exactly cover the input is a returned error — this
// never silently returns fewer/misaligned vectors than requested. The API key
// (when set) is never included in any returned error.
func (e *APIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("semantic: marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("semantic: build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	out, err := e.doEmbed(req)
	if err != nil {
		return nil, err
	}
	return alignEmbeddings(out, len(texts))
}

// doEmbed performs req and decodes a successful response into an
// embedResponse, translating a non-2xx status or a transport/decode failure
// into a descriptive error. It never includes the request's Authorization
// header in any returned error.
func (e *APIEmbedder) doEmbed(req *http.Request) (embedResponse, error) {
	var out embedResponse

	resp, err := e.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("semantic: embed request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return out, fmt.Errorf("semantic: embed status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("semantic: decode embed response: %w", err)
	}
	return out, nil
}

// alignEmbeddings reorders an embedResponse's Data entries by Index into an
// input-order slice of length want, and validates that every position was
// filled exactly once — catching a provider that returns too few/many
// vectors or an out-of-range/duplicate Index before it can silently
// corrupt a Point's Vector downstream.
func alignEmbeddings(out embedResponse, want int) ([][]float32, error) {
	if len(out.Data) != want {
		return nil, fmt.Errorf("semantic: embed response returned %d vectors for %d inputs", len(out.Data), want)
	}
	result := make([][]float32, want)
	filled := make([]bool, want)
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= want {
			return nil, fmt.Errorf("semantic: embed response index %d out of range for %d inputs", d.Index, want)
		}
		if filled[d.Index] {
			return nil, fmt.Errorf("semantic: embed response duplicate index %d", d.Index)
		}
		result[d.Index] = d.Embedding
		filled[d.Index] = true
	}
	return result, nil
}
