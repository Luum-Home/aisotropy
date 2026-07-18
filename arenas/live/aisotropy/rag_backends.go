// rag_backends.go carries the small, stdlib-only HTTP clients the rag mode uses
// to (a) load the shared Nimbus-Rail corpus, and (b) MIRROR aisotropy's own
// semantic retrieval so the `retrieved` field reflects exactly what the runtime's
// hydration pulled: the query is embedded with the SAME e5 server + model id
// aisotropy's APIEmbedder uses, and the search hits the SAME real Qdrant
// collection with the SAME (tenant_id, user_id, kind) payload filter aisotropy's
// semantic.QdrantStore applies during the HNSW scan. It never invents scores —
// they are Qdrant's cosine similarities.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// corpusDir resolves the shared corpus directory. AISOTROPY_ARENA_CORPUS_DIR
// wins (run.sh / Dockerfile set it); otherwise it falls back to the repo-relative
// path from the adapter directory.
func corpusDir() string {
	if d := os.Getenv("AISOTROPY_ARENA_CORPUS_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", "_shared", "corpus")
}

// loadCorpus reads every *.md passage into an aisotropy.Document (ID + Source =
// filename, Content = full passage). Each file's first "# Title" line becomes the
// title surfaced on retrieved docs.
func loadCorpus() ([]aisotropy.Document, map[string]string, error) {
	dir := corpusDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading corpus dir %q: %w", dir, err)
	}
	var docs []aisotropy.Document
	titles := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, nil, fmt.Errorf("reading corpus file %q: %w", e.Name(), err)
		}
		content := string(b)
		docs = append(docs, aisotropy.Document{ID: e.Name(), Content: content, Source: e.Name()})
		titles[e.Name()] = titleOf(content)
	}
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("no *.md passages found in corpus dir %q", dir)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs, titles, nil
}

func titleOf(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
		if line != "" {
			return line
		}
	}
	return ""
}

// ---- e5 query embedding (mirrors aisotropy's APIEmbedder request) ----

func embedURL() string {
	if u := os.Getenv("AISOTROPY_EMBED_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://127.0.0.1:8899"
}

func embedModel() string {
	if m := os.Getenv("AISOTROPY_EMBED_MODEL"); m != "" {
		return m
	}
	return "multilingual-e5-small-query"
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// embedQuery embeds one query string via the real e5 server with the query model
// id (so the server applies the "query:" e5 prefix), exactly as aisotropy's
// APIEmbedder does for its own retrieval.
func embedQuery(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{"model": embedModel(), "input": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embedURL()+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("AISOTROPY_EMBED_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed server status %d", resp.StatusCode)
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(er.Data) != 1 {
		return nil, fmt.Errorf("embed server returned %d vectors, want 1", len(er.Data))
	}
	return er.Data[0].Embedding, nil
}

// ---- Qdrant REST search (mirrors aisotropy's semantic.QdrantStore.Search) ----

func qdrantRESTURL() string {
	if u := os.Getenv("QDRANT_REST_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	host := os.Getenv("QDRANT_HOST")
	if host == "" {
		host = "localhost"
	}
	return "http://" + host + ":6333"
}

func qdrantCollection() string {
	if c := os.Getenv("QDRANT_COLLECTION"); c != "" {
		return c
	}
	return "aisotropy_arena"
}

// qdrantSearch runs the SAME ANN query aisotropy's semantic tier runs for chat
// hydration: filtered to (tenant_id, user_id, kind=document) during the scan. The
// scores are Qdrant's own cosine similarities.
func qdrantSearch(ctx context.Context, vec []float32, tenant, user string, topK int) ([]retrievedDoc, error) {
	payload := map[string]any{
		"vector":       vec,
		"limit":        topK,
		"with_payload": true,
		"filter": map[string]any{
			"must": []map[string]any{
				{"key": "tenant_id", "match": map[string]any{"value": tenant}},
				{"key": "user_id", "match": map[string]any{"value": user}},
				{"key": "kind", "match": map[string]any{"value": "document"}},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/collections/%s/points/search", qdrantRESTURL(), qdrantCollection())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("QDRANT_API_KEY"); key != "" {
		req.Header.Set("api-key", key)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant search request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qdrant search status %d", resp.StatusCode)
	}
	var sr struct {
		Result []struct {
			Score   float64 `json:"score"`
			Payload struct {
				Content string `json:"content"`
				Source  string `json:"source"`
			} `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode qdrant search response: %w", err)
	}
	out := make([]retrievedDoc, 0, len(sr.Result))
	for _, r := range sr.Result {
		out = append(out, retrievedDoc{
			DocID: r.Payload.Source,
			Title: titleOf(r.Payload.Content),
			Score: r.Score,
			Text:  r.Payload.Content,
		})
	}
	return out, nil
}
