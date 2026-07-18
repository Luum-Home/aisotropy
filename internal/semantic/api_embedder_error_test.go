package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPIEmbedderTransportErrorIsWrapped verifies a transport-level failure
// (the server is unreachable) surfaces as a returned error from doEmbed, never
// a panic or an empty result.
func TestAPIEmbedderTransportErrorIsWrapped(t *testing.T) {
	// Start then immediately close a server so its URL is a valid but
	// dead endpoint: e.http.Do fails at the transport layer.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: url, Dimension: 4})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected a transport error against a dead server, got nil")
	}
}

// TestAPIEmbedderMalformedBodyIsError verifies a 200 response whose body is not
// valid JSON is a returned decode error, not a silent empty result.
func TestAPIEmbedderMalformedBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 4})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected a decode error on a malformed body, got nil")
	}
}

// TestAPIEmbedderOutOfRangeIndexIsError verifies alignEmbeddings rejects a
// response whose Data count matches the input count but whose Index field is
// out of range — a provider bug that must not corrupt a downstream vector.
func TestAPIEmbedderOutOfRangeIndexIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{Index: 5, Embedding: []float32{1, 2, 3, 4}}}} // len==1==want, but index 5 is out of range
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 4})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"only-one"}); err == nil {
		t.Fatal("Embed: expected an out-of-range index error, got nil")
	}
}

// TestAPIEmbedderBuildRequestErrorIsWrapped verifies that a baseURL which
// fails http.NewRequestWithContext's URL validation (a raw ASCII control
// character, here DEL/0x7f) surfaces as a wrapped "build embed request"
// error rather than a panic. NewAPIEmbedder only validates BaseURL is
// non-empty, not that it parses as a URL, so this is a legitimately reachable
// failure — unlike the json.Marshal error two lines above it in Embed, which
// cannot fail for an embedRequest of plain strings.
func TestAPIEmbedderBuildRequestErrorIsWrapped(t *testing.T) {
	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: "http://example.com/\x7f", Dimension: 3})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected a build-request error for an invalid URL, got nil")
	}
}

// TestAPIEmbedderDuplicateIndexIsError verifies alignEmbeddings rejects a
// response that fills the same Index twice (and therefore leaves another
// position empty) even though the Data count matches the input count.
func TestAPIEmbedderDuplicateIndexIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{
			{Index: 0, Embedding: []float32{1, 2}},
			{Index: 0, Embedding: []float32{3, 4}}, // duplicate index 0, position 1 never filled
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb, err := NewAPIEmbedder(APIEmbedderConfig{BaseURL: srv.URL, Dimension: 2})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("Embed: expected a duplicate-index error, got nil")
	}
}
