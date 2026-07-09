package semantic

import (
	"context"
	"os"
	"strconv"
)

// OpenFromEnv builds the semantic Store from the environment, mirroring the
// other memory tiers' config precedence:
//
//   - QDRANT_HOST set -> networked QdrantStore (gRPC; ADR-0001)
//   - none            -> in-memory InMemoryStore (zero-config default)
//
// dim must equal the Embedder's Dimension(). Extra Qdrant settings:
// QDRANT_PORT (default 6334), QDRANT_API_KEY, QDRANT_USE_TLS ("true"),
// QDRANT_COLLECTION (default "aisotropy_knowledge").
func OpenFromEnv(ctx context.Context, dim int) (Store, error) {
	host := os.Getenv("QDRANT_HOST")
	if host == "" {
		return NewInMemoryStore(dim), nil
	}
	port := 6334
	if p := os.Getenv("QDRANT_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	}
	return OpenQdrant(ctx, QdrantConfig{
		Host:       host,
		Port:       port,
		APIKey:     os.Getenv("QDRANT_API_KEY"),
		UseTLS:     os.Getenv("QDRANT_USE_TLS") == "true",
		Collection: os.Getenv("QDRANT_COLLECTION"),
		Dim:        dim,
	})
}
