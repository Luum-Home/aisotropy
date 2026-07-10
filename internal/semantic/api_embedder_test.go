package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIEmbedderEmbedRequestShapeAndOrder verifies Embed sends the expected
// OpenAI-compatible request (model + input array + Bearer auth header) and
// parses the response back into input order, honoring the response's Index
// field even when the provider does not echo Data back in request order.
func TestAPIEmbedderEmbedRequestShapeAndOrder(t *testing.T) {
	const apiKey = "sk-test-secret-12345"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type header = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization header = %q, want Bearer %s", got, apiKey)
		}

		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("model = %q, want test-model", req.Model)
		}
		if len(req.Input) != 2 || req.Input[0] != "alpha" || req.Input[1] != "beta" {
			t.Fatalf("input = %#v, want [alpha beta]", req.Input)
		}

		// Deliberately return Data out of request order to prove Embed
		// reorders by Index rather than trusting array position.
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{
			{Index: 1, Embedding: []float32{0.4, 0.5}},
			{Index: 0, Embedding: []float32{0.1, 0.2}},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{
		BaseURL:   srv.URL,
		Model:     "test-model",
		APIKey:    apiKey,
		Dimension: 2,
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if got := emb.Dimension(); got != 2 {
		t.Fatalf("Dimension() = %d, want 2", got)
	}

	vecs, err := emb.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len(vecs) = %d, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != emb.Dimension() {
			t.Errorf("vecs[%d] has dimension %d, want configured dimension %d", i, len(v), emb.Dimension())
		}
	}
	if vecs[0][0] != 0.1 || vecs[0][1] != 0.2 {
		t.Errorf("vecs[0] = %v, want [0.1 0.2] (input order preserved)", vecs[0])
	}
	if vecs[1][0] != 0.4 || vecs[1][1] != 0.5 {
		t.Errorf("vecs[1] = %v, want [0.4 0.5] (input order preserved)", vecs[1])
	}
}

// TestAPIEmbedderNoAPIKeyOmitsAuthHeader verifies the Authorization header is
// entirely absent when no API key is configured — required for local
// embedding servers (Ollama, text-embeddings-inference) that accept no auth.
func TestAPIEmbedderNoAPIKeyOmitsAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header = %q, want empty (no api key configured)", got)
		}
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{Index: 0, Embedding: []float32{1, 2, 3}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 3})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"solo"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
}

// TestAPIEmbedderDefaultModel verifies the Model field defaults to
// defaultAPIEmbedModel when the config leaves it empty.
func TestAPIEmbedderDefaultModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{Index: 0, Embedding: []float32{1}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 1})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotModel != defaultAPIEmbedModel {
		t.Errorf("model = %q, want default %q", gotModel, defaultAPIEmbedModel)
	}
}

// TestAPIEmbedderNonOKStatusIsError verifies a non-2xx response is returned
// as an error (not a panic, not a silently empty result), and that the
// configured API key never leaks into the error string.
func TestAPIEmbedderNonOKStatusIsError(t *testing.T) {
	const apiKey = "sk-super-secret-should-not-leak"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid api key"))
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, APIKey: apiKey, Dimension: 4})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}

	_, err = emb.Embed(context.Background(), []string{"boom"})
	if err == nil {
		t.Fatal("Embed: expected error on 401 response, got nil")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error string leaks the api key: %q", err.Error())
	}
}

// TestAPIEmbedderMismatchedResponseCountIsError verifies a provider that
// returns a different number of vectors than requested is a returned error,
// not a silently misaligned result.
func TestAPIEmbedderMismatchedResponseCountIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{Index: 0, Embedding: []float32{1}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 1})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("Embed: expected error on vector-count mismatch, got nil")
	}
}

// TestNewAPIEmbedderValidation verifies the required-field checks: an empty
// BaseURL or a non-positive Dimension must error, never silently construct a
// broken embedder.
func TestNewAPIEmbedderValidation(t *testing.T) {
	if _, err := NewAPIEmbedder(APIEmbedderConfig{Dimension: 4}); err == nil {
		t.Error("empty BaseURL: expected error, got nil")
	}
	if _, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: "http://localhost:1234"}); err == nil {
		t.Error("zero Dimension: expected error, got nil")
	}
	if _, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: "http://localhost:1234", Dimension: -1}); err == nil {
		t.Error("negative Dimension: expected error, got nil")
	}
}

// TestAPIEmbedderEmbedEmptyInputIsNoOp verifies Embed short-circuits on an
// empty input slice without making a request.
func TestAPIEmbedderEmbedEmptyInputIsNoOp(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 4})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	vecs, err := emb.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("len(vecs) = %d, want 0", len(vecs))
	}
	if called {
		t.Error("Embed made an HTTP request for an empty input slice")
	}
}

// TestEmbedderFromEnvDefaultsToHashEmbedder verifies EmbedderFromEnv returns
// a HashEmbedder when AISOTROPY_EMBED_URL is unset — the credential-less
// default, mirroring llm.FromEnv's stub fallback.
func TestEmbedderFromEnvDefaultsToHashEmbedder(t *testing.T) {
	t.Setenv("AISOTROPY_EMBED_URL", "")

	emb := EmbedderFromEnv()
	if _, ok := emb.(HashEmbedder); !ok {
		t.Fatalf("EmbedderFromEnv() = %T, want HashEmbedder", emb)
	}
}

// TestEmbedderFromEnvBuildsAPIEmbedderWhenURLSet verifies EmbedderFromEnv
// returns a real APIEmbedder, configured from env, when AISOTROPY_EMBED_URL
// is set — and that it is actually usable end to end against a mock server.
func TestEmbedderFromEnvBuildsAPIEmbedderWhenURLSet(t *testing.T) {
	var gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		gotAuth = r.Header.Get("Authorization")
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{Index: 0, Embedding: []float32{1, 2, 3, 4}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("AISOTROPY_EMBED_URL", srv.URL)
	t.Setenv("AISOTROPY_EMBED_MODEL", "my-embed-model")
	t.Setenv("AISOTROPY_EMBED_API_KEY", "env-key")
	t.Setenv("AISOTROPY_EMBED_DIM", "4")

	emb := EmbedderFromEnv()
	api, ok := emb.(*APIEmbedder)
	if !ok {
		t.Fatalf("EmbedderFromEnv() = %T, want *APIEmbedder", emb)
	}
	if got := api.Dimension(); got != 4 {
		t.Fatalf("Dimension() = %d, want 4", got)
	}

	if _, err := api.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotModel != "my-embed-model" {
		t.Errorf("model sent = %q, want my-embed-model", gotModel)
	}
	if gotAuth != "Bearer env-key" {
		t.Errorf("Authorization = %q, want Bearer env-key", gotAuth)
	}
}

// TestEmbedderFromEnvFallsBackToDefaultDimWhenUnset verifies a missing/
// invalid AISOTROPY_EMBED_DIM degrades to defaultEmbedDim rather than
// producing a zero-dimension (broken) embedder.
func TestEmbedderFromEnvFallsBackToDefaultDimWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(embedResponse{})
	}))
	defer srv.Close()

	t.Setenv("AISOTROPY_EMBED_URL", srv.URL)
	t.Setenv("AISOTROPY_EMBED_DIM", "")

	emb := EmbedderFromEnv()
	api, ok := emb.(*APIEmbedder)
	if !ok {
		t.Fatalf("EmbedderFromEnv() = %T, want *APIEmbedder", emb)
	}
	if got := api.Dimension(); got != defaultEmbedDim {
		t.Fatalf("Dimension() = %d, want default %d", got, defaultEmbedDim)
	}
}
