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
	"github.com/cloudwego/eino/components/model"
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

	// stub is true when ARENA_STUB=1 — every mode routes through the local,
	// network-free fake chat model (stub.go) instead of the real agnes
	// endpoint, and memory-backed modes default to an in-memory SQLite store
	// instead of an on-disk file. See README.md#stub-mode.
	stub bool
}

func loadEnv() (*arenaEnv, error) {
	stub := os.Getenv("ARENA_STUB") == "1"

	defaultMemoryDBPath := ".data/memory.db"
	if stub {
		// In-memory store: every process invocation starts from a clean
		// slate (no cross-process persistence — that is inherent to
		// "in-memory", not a bug; see README.md#stub-mode for what this
		// means for memory/memory_scoped/crash_recovery under ARENA_STUB).
		defaultMemoryDBPath = ":memory:"
	}

	env := &arenaEnv{
		apiKey:  os.Getenv("OPENAI_API_KEY"),
		baseURL: os.Getenv("OPENAI_BASE_URL"),
		model:   os.Getenv("OPENAI_MODEL"),
		stub:    stub,

		embedURL:   getenvDefault("EMBED_URL", "http://127.0.0.1:8899"),
		embedModel: getenvDefault("EMBED_MODEL", "multilingual-e5-small-query"),

		qdrantURL:        getenvDefault("QDRANT_URL", "http://localhost:6333"),
		qdrantCollection: getenvDefault("QDRANT_COLLECTION", "arena_rag"),

		memoryDBPath: getenvDefault("EINO_ARENA_MEMORY_DB", defaultMemoryDBPath),
	}

	if stub {
		// Offline: no real agnes endpoint is ever dialed, so OPENAI_* is not
		// required. ALWAYS report the offline stub model on the wire contract,
		// never the real OPENAI_MODEL: run.sh sets OPENAI_MODEL even for a stub
		// run, so overriding only-when-empty left the real "agnes-2.0-flash" on
		// the wire (the runner's lane-leak guard reads this field).
		env.model = "arena-stub-echo"
	} else {
		if env.apiKey == "" {
			return nil, errors.New("OPENAI_API_KEY is not set")
		}
		if env.model == "" {
			return nil, errors.New("OPENAI_MODEL is not set")
		}
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

// newChatModel constructs the chat model every mode drives: Eino's REAL
// OpenAI-compatible chat model component pointed at agnes-2.0-flash
// (einoopenai.NewChatModel overrides the vendored go-openai client's BaseURL
// so it talks to any OpenAI-compatible endpoint — the exact mechanism the
// original single-shot adapter already used), UNLESS env.stub is true
// (ARENA_STUB=1), in which case it returns the local, network-free fake
// model.ToolCallingChatModel from stub.go instead — see README.md#stub-mode.
//
// The return type is the model.ToolCallingChatModel INTERFACE (Generate +
// Stream + WithTools), not the concrete *einochatopenai.ChatModel struct, so
// every call site (mode_qa.go, mode_tools.go, mode_rag.go, mode_memory.go,
// mode_structured.go, mode_stream.go, mode_scoped_memory.go, mode_crash.go,
// mode_observability.go, mode_middleware.go) is unaffected by which
// implementation is behind it — none of them use anything outside this
// interface, so this single choke point is where ARENA_STUB fans out to
// every mode without a single mode-specific code change.
func newChatModel(ctx context.Context, env *arenaEnv, cfg *einochatopenai.ChatModelConfig) (model.ToolCallingChatModel, error) {
	if env.stub {
		return newStubChatModel(), nil
	}
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
		// The arena's Qdrant runs with QDRANT__SERVICE__API_KEY. Omitting this
		// makes every retrieval fail Unauthenticated, which scores as a RAG miss
		// and reads as a framework weakness rather than an adapter omission.
		APIKey: os.Getenv("QDRANT_API_KEY"),
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
