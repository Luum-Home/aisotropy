package arena.springai;

import java.util.List;
import java.util.Map;

import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * Per-invocation context: the parsed request plus the resolved endpoints for
 * the real infra tiers. Built once in {@link Adapter#main} and threaded into
 * whichever {@link Modes} handler runs.
 */
final class Ctx {

    final ObjectMapper mapper;
    final Map<String, Object> in;
    final String modelName;
    final String mode;
    final boolean stub;

    // Real-infra endpoints (identical env-var names + defaults as the sibling
    // adapters: EMBED_URL / QDRANT_URL / RQLITE_URL, plus the runner-injected
    // QDRANT_COLLECTION/ARENA_COLLECTION). See arenas/live/genkit-go/main.go.
    final String apiKey;
    final String chatBaseUrl;   // normalized: OpenAiApi appends /v1/chat/completions
    final String embedBaseUrl;  // normalized: OpenAiApi appends /v1/embeddings
    final String qdrantHost;
    final int qdrantGrpcPort;
    final String collection;
    final String rqliteUrl;

    private Ctx(ObjectMapper mapper, Map<String, Object> in, String modelName, String mode, boolean stub,
                String apiKey, String chatBaseUrl, String embedBaseUrl, String qdrantHost, int qdrantGrpcPort,
                String collection, String rqliteUrl) {
        this.mapper = mapper;
        this.in = in;
        this.modelName = modelName;
        this.mode = mode;
        this.stub = stub;
        this.apiKey = apiKey;
        this.chatBaseUrl = chatBaseUrl;
        this.embedBaseUrl = embedBaseUrl;
        this.qdrantHost = qdrantHost;
        this.qdrantGrpcPort = qdrantGrpcPort;
        this.collection = collection;
        this.rqliteUrl = rqliteUrl;
    }

    static Ctx fromEnv(ObjectMapper mapper, Map<String, Object> in, String modelName, String mode) {
        boolean stub = "1".equals(System.getenv("ARENA_STUB"));

        String apiKey = "";
        String chatBaseUrl = "";
        if (!stub) {
            apiKey = System.getenv("OPENAI_API_KEY");
            if (apiKey == null || apiKey.isEmpty()) {
                throw new IllegalStateException(
                        "OPENAI_API_KEY is not set (set ARENA_STUB=1 to run fully offline against a fake model)");
            }
            String base = System.getenv("OPENAI_BASE_URL");
            if (base == null || base.isEmpty()) {
                throw new IllegalStateException(
                        "OPENAI_BASE_URL is not set (set ARENA_STUB=1 to run fully offline against a fake model)");
            }
            chatBaseUrl = normalizeBase(base);
        }

        // e5 embed server (shared, OpenAI-compatible). No /v1 in its default
        // URL, so normalization leaves it as-is and OpenAiApi appends
        // /v1/embeddings — the e5 server serves both /embeddings and
        // /v1/embeddings (arenas/live/_shared/README.md).
        String embedBaseUrl = normalizeBase(Adapter.envOr("EMBED_URL", "http://127.0.0.1:8899"));

        // Qdrant is reached over gRPC (host:grpcPort). QDRANT_URL is REST-shaped
        // (default http://localhost:6333); gRPC port = REST + 1 by the project
        // convention (internal/semantic/qdrant.go: REST 6333 / gRPC 6334),
        // overridable via QDRANT_GRPC_PORT. Same derivation as
        // arenas/live/genkit-go/qdrant_client.go.
        String[] hp = parseQdrantHostPort(Adapter.envOr("QDRANT_URL", "http://localhost:6333"));
        String qdrantHost = hp[0];
        int restPort = Integer.parseInt(hp[1]);
        int grpcPort = restPort + 1;
        String grpcOverride = System.getenv("QDRANT_GRPC_PORT");
        if (grpcOverride != null && !grpcOverride.isEmpty()) {
            grpcPort = Integer.parseInt(grpcOverride);
        }

        // The runner injects a unique per-framework collection
        // (arena_rag_spring_ai) via QDRANT_COLLECTION/ARENA_COLLECTION; the
        // literal below is only the standalone-run fallback.
        String collection = firstNonEmpty(
                System.getenv("QDRANT_COLLECTION"),
                System.getenv("ARENA_COLLECTION"),
                "arena_rag_spring_ai");

        String rqliteUrl = Adapter.envOr("RQLITE_URL", "http://localhost:4001");

        return new Ctx(mapper, in, modelName, mode, stub, apiKey, chatBaseUrl, embedBaseUrl,
                qdrantHost, grpcPort, collection, rqliteUrl);
    }

    // ---- request accessors ----

    String system() {
        return Adapter.strOr(in.get("system"), "");
    }

    String sessionId() {
        return Adapter.strOr(in.get("session_id"), "");
    }

    int topK() {
        Object v = in.get("top_k");
        if (v instanceof Number n && n.intValue() > 0) {
            return n.intValue();
        }
        return 4;
    }

    @SuppressWarnings("unchecked")
    List<Map<String, Object>> messages() {
        Object v = in.get("messages");
        return (v instanceof List<?> l) ? (List<Map<String, Object>>) l : List.of();
    }

    @SuppressWarnings("unchecked")
    List<Map<String, Object>> tools() {
        Object v = in.get("tools");
        return (v instanceof List<?> l) ? (List<Map<String, Object>>) l : List.of();
    }

    // ---- endpoint parsing helpers ----

    /**
     * Strips a trailing {@code /v1} (and any trailing slash) so Spring AI's
     * OpenAiApi, which appends {@code v1/chat/completions} / {@code v1/embeddings}
     * itself, does not produce a doubled {@code /v1/v1/...} path. The arena's
     * OPENAI_BASE_URL is documented as e.g. {@code https://apihub.agnes-ai.com/v1}.
     */
    static String normalizeBase(String url) {
        String u = url.trim();
        while (u.endsWith("/")) {
            u = u.substring(0, u.length() - 1);
        }
        if (u.endsWith("/v1")) {
            u = u.substring(0, u.length() - 3);
        }
        return u;
    }

    /** Returns {host, restPort} from a URL like {@code http://localhost:6333}. */
    static String[] parseQdrantHostPort(String qdrantUrl) {
        String u = qdrantUrl.trim();
        int scheme = u.indexOf("://");
        if (scheme >= 0) {
            u = u.substring(scheme + 3);
        }
        int slash = u.indexOf('/');
        if (slash >= 0) {
            u = u.substring(0, slash);
        }
        String host = u;
        String port = "6333";
        int colon = u.lastIndexOf(':');
        if (colon >= 0) {
            host = u.substring(0, colon);
            port = u.substring(colon + 1);
        }
        if (host.isEmpty()) {
            host = "localhost";
        }
        return new String[] { host, port };
    }

    static String firstNonEmpty(String... vals) {
        for (String v : vals) {
            if (v != null && !v.isEmpty()) {
                return v;
            }
        }
        return "";
    }
}
