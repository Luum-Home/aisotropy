package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// embedRequest/embedResponse mirror the OpenAI-compatible embeddings shape
// served by arenas/live/_shared/embed-server (a real intfloat/multilingual-e5-small
// server, 384-dim). See arenas/live/_shared/README.md "The embed server".
type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// embedQuery embeds a search query with the e5 "query:" role via the
// "...-query" model-id suffix (see _shared/README.md "e5 prefixes"). Returns
// a 384-dim unit vector.
func embedQuery(ctx context.Context, embedURL, text string) ([]float32, error) {
	return embed(ctx, embedURL, "multilingual-e5-small-query", text)
}

func embed(ctx context.Context, embedURL, model, text string) ([]float32, error) {
	if stubMode {
		return stubEmbedding(text), nil
	}
	body, err := json.Marshal(embedRequest{Model: model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embedURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: POST %s/embeddings: %w", embedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: %s returned HTTP %d", embedURL, resp.StatusCode)
	}
	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embed: empty data in response")
	}
	return out.Data[0].Embedding, nil
}
