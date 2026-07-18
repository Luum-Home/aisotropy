package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// searchQdrant runs a real ANN search against the "arena_rag" collection
// (arenas/live/_shared/populate-qdrant.py) using github.com/qdrant/go-client
// (Apache-2.0) over gRPC. Genkit-Go v1.10.0 ships no first-party Qdrant
// retriever/indexer plugin, so this adapter talks to Qdrant directly — see
// README.md "Why go-client, not a Genkit plugin" — while generation itself
// still goes through a Genkit flow (rag.go).
//
// qdrantURL is the REST-shaped env value (QDRANT_URL, default
// http://localhost:6333); go-client speaks gRPC, whose port is REST+1 by the
// project's own convention (internal/semantic/qdrant.go: REST 6333 / gRPC
// 6334, also the docker-compose.yml mapping) unless QDRANT_GRPC_PORT
// overrides it explicitly. Only the host is taken from qdrantURL so a single
// env var change (e.g. host.docker.internal for the Docker packaging) moves
// both the embed-side REST convention and this gRPC client together.
func searchQdrant(ctx context.Context, qdrantURL, collection string, vector []float32, limit int) ([]retrievedDoc, error) {
	if stubMode {
		return stubQdrantSearch(limit), nil
	}
	host, restPort, err := parseQdrantHost(qdrantURL)
	if err != nil {
		return nil, fmt.Errorf("qdrant: %w", err)
	}
	grpcPort := restPort + 1
	if p := os.Getenv("QDRANT_GRPC_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("qdrant: invalid QDRANT_GRPC_PORT %q: %w", p, err)
		}
		grpcPort = v
	}

	client, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: grpcPort})
	if err != nil {
		return nil, fmt.Errorf("qdrant: connect %s:%d: %w", host, grpcPort, err)
	}
	defer client.Close()

	lim := uint64(limit)
	res, err := client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: collection,
		Query:          qdrant.NewQuery(vector...),
		Limit:          &lim,
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: query collection %q: %w", collection, err)
	}

	out := make([]retrievedDoc, 0, len(res))
	for _, r := range res {
		payload := r.GetPayload()
		out = append(out, retrievedDoc{
			DocID: payload["doc_id"].GetStringValue(),
			Title: payload["title"].GetStringValue(),
			Text:  payload["text"].GetStringValue(),
			Score: r.GetScore(),
		})
	}
	return out, nil
}

// parseQdrantHost extracts the host and REST port from a QDRANT_URL like
// "http://localhost:6333". Defaults the port to 6333 if the URL omits one.
func parseQdrantHost(qdrantURL string) (host string, restPort int, err error) {
	u, err := url.Parse(qdrantURL)
	if err != nil {
		return "", 0, fmt.Errorf("parse QDRANT_URL %q: %w", qdrantURL, err)
	}
	host = u.Hostname()
	if host == "" {
		host = strings.TrimSuffix(strings.TrimPrefix(qdrantURL, "http://"), "/")
	}
	restPort = 6333
	if p := u.Port(); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("parse QDRANT_URL port %q: %w", p, err)
		}
		restPort = v
	}
	return host, restPort, nil
}
