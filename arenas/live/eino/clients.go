package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"

	einoembedopenai "github.com/cloudwego/eino-ext/components/embedding/openai"
	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/qdrant/go-client/qdrant"
)

// arenaEnv holds every piece of REAL-infrastructure configuration this
// adapter needs, read once from the environment and threaded explicitly
// through each mode handler. Nothing here is hardcoded — every host is
// overridable so the same binary works unchanged inside a Docker container
// reaching the host via host.docker.internal.
type arenaEnv struct {
	// agnes chat model (required for every mode)
	apiKey  string
	baseURL string
	model   string

	// _shared real e5 embeddings server (rag mode)
	embedURL   string
	embedModel string

	// real Qdrant (rag mode)
	qdrantURL        string
	qdrantGRPCPort   int
	qdrantCollection string

	// on-disk SQLite (memory mode)
	memoryDBPath string
}

func loadEnv() (*arenaEnv, error) {
	env := &arenaEnv{
		apiKey:  os.Getenv("OPENAI_API_KEY"),
		baseURL: os.Getenv("OPENAI_BASE_URL"),
		model:   os.Getenv("OPENAI_MODEL"),

		embedURL:   getenvDefault("EMBED_URL", "http://127.0.0.1:8899"),
		embedModel: getenvDefault("EMBED_MODEL", "multilingual-e5-small-query"),

		qdrantURL:        getenvDefault("QDRANT_URL", "http://localhost:6333"),
		qdrantCollection: getenvDefault("QDRANT_COLLECTION", "arena_rag"),

		memoryDBPath: getenvDefault("EINO_ARENA_MEMORY_DB", ".data/memory.db"),
	}

	if env.apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is not set")
	}
	if env.model == "" {
		return nil, errors.New("OPENAI_MODEL is not set")
	}

	grpcPort := 6334
	if p := os.Getenv("QDRANT_GRPC_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("QDRANT_GRPC_PORT: invalid port %q: %w", p, err)
		}
		grpcPort = v
	}
	env.qdrantGRPCPort = grpcPort

	return env, nil
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newChatModel constructs Eino's real OpenAI-compatible chat model component
// pointed at agnes-2.0-flash. This is the exact mechanism the single-shot
// adapter already used: einoopenai.NewChatModel overrides the vendored
// go-openai client's BaseURL so it talks to any OpenAI-compatible endpoint.
func newChatModel(ctx context.Context, env *arenaEnv, cfg *einochatopenai.ChatModelConfig) (*einochatopenai.ChatModel, error) {
	if cfg == nil {
		cfg = &einochatopenai.ChatModelConfig{}
	}
	cfg.APIKey = env.apiKey
	cfg.BaseURL = env.baseURL
	cfg.Model = env.model
	chatModel, err := einochatopenai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("constructing Eino OpenAI chat model: %w", err)
	}
	return chatModel, nil
}

// newQueryEmbedder constructs Eino's native OpenAI-compatible embedding
// component pointed at the real e5 server. The e5 server picks the query/
// passage instruction prefix from the model-id suffix (see
// arenas/live/_shared/README.md#e5-prefixes), so Model is always the
// "...-query" variant here — this adapter only ever embeds search queries,
// never indexes documents (the corpus is indexed once, offline, by
// populate-qdrant.py).
func newQueryEmbedder(ctx context.Context, env *arenaEnv) (*einoembedopenai.Embedder, error) {
	emb, err := einoembedopenai.NewEmbedder(ctx, &einoembedopenai.EmbeddingConfig{
		APIKey:  "unused-local-embed-server", // the e5 server does not check auth; go-openai requires a non-empty key to build the client
		BaseURL: env.embedURL,
		Model:   env.embedModel,
	})
	if err != nil {
		return nil, fmt.Errorf("constructing Eino OpenAI embedder for %s: %w", env.embedURL, err)
	}
	return emb, nil
}

// newQdrantClient connects the real gRPC Qdrant client. QDRANT_URL is the
// REST-style base URL (e.g. http://localhost:6333 or
// http://host.docker.internal:6333 inside a container); the gRPC port is
// derived (default 6334, i.e. REST+1, matching this repo's own
// internal/semantic convention and the reference docker-compose.yml) unless
// QDRANT_GRPC_PORT overrides it explicitly.
func newQdrantClient(env *arenaEnv) (*qdrant.Client, error) {
	u, err := url.Parse(env.qdrantURL)
	if err != nil {
		return nil, fmt.Errorf("parsing QDRANT_URL %q: %w", env.qdrantURL, err)
	}
	host := u.Hostname()
	if host == "" {
		host = env.qdrantURL // QDRANT_URL was given as a bare host, not a URL
	}
	client, err := qdrant.NewClient(&qdrant.Config{
		Host: host,
		Port: env.qdrantGRPCPort,
		// The local reference Qdrant may run an older server than the
		// pinned go-client's expected version skew; this doesn't affect the
		// search/query wire calls this adapter uses.
		SkipCompatibilityCheck: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting qdrant %s:%d: %w", host, env.qdrantGRPCPort, err)
	}
	return client, nil
}
