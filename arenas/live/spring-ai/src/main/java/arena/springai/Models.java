package arena.springai;

import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.document.MetadataMode;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.openai.OpenAiChatModel;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.ai.openai.OpenAiEmbeddingModel;
import org.springframework.ai.openai.OpenAiEmbeddingOptions;
import org.springframework.ai.openai.api.OpenAiApi;
import org.springframework.ai.vectorstore.VectorStore;
import org.springframework.ai.vectorstore.qdrant.QdrantVectorStore;

import io.qdrant.client.QdrantClient;
import io.qdrant.client.QdrantGrpcClient;

/**
 * Factory that builds Spring AI's native model + store components — either the
 * REAL agnes/e5/Qdrant clients or the offline {@link StubChatModel} /
 * {@link StubEmbeddingModel} when {@code ARENA_STUB=1}. The stub lane makes ZERO
 * network calls.
 */
final class Models {

    private Models() {
    }

    /** agnes-2.0-flash over its OpenAI-compatible endpoint, or the offline stub. */
    static ChatModel chatModel(Ctx ctx) {
        if (ctx.stub) {
            return new StubChatModel(ctx.modelName);
        }
        // apiKey(String) wraps into a SimpleApiKey; the key is never logged.
        OpenAiApi api = OpenAiApi.builder()
                .baseUrl(ctx.chatBaseUrl)
                .apiKey(ctx.apiKey)
                .build();
        OpenAiChatOptions opts = OpenAiChatOptions.builder()
                .model(ctx.modelName)
                .build();
        return OpenAiChatModel.builder()
                .openAiApi(api)
                .defaultOptions(opts)
                .build();
    }

    /**
     * The shared e5 embedder (multilingual-e5-small, 384-dim) over its
     * OpenAI-compatible endpoint, using the "query" role suffix so the query
     * side of the search uses e5's {@code query:} prefix (the corpus was indexed
     * with {@code passage:} by populate-qdrant.py) — see _shared/README.md.
     */
    static EmbeddingModel embeddingModel(Ctx ctx) {
        if (ctx.stub) {
            return new StubEmbeddingModel();
        }
        OpenAiApi embedApi = OpenAiApi.builder()
                .baseUrl(ctx.embedBaseUrl)
                .apiKey("not-needed")
                .build();
        OpenAiEmbeddingOptions opts = OpenAiEmbeddingOptions.builder()
                .model("multilingual-e5-small-query")
                .build();
        return new OpenAiEmbeddingModel(embedApi, MetadataMode.EMBED, opts);
    }

    /**
     * QdrantVectorStore over the shared, pre-seeded per-framework collection
     * (arena_rag_spring_ai) via the official io.qdrant gRPC client. Schema
     * initialization is off: the collection is seeded out-of-band by
     * _shared/populate-qdrant.py, this adapter only searches it. Real lane only
     * — the stub lane never constructs a VectorStore (see {@link Modes#rag}).
     */
    /**
     * Builds the gRPC channel, applying QDRANT_API_KEY when the environment
     * supplies one. The arena's Qdrant runs with QDRANT__SERVICE__API_KEY set;
     * without the key every search fails Unauthenticated, which the runner
     * scores as a retrieval miss — i.e. it would read as a Spring AI weakness
     * when it is purely an omission in this adapter. Empty/unset key keeps the
     * previous anonymous behaviour, so a keyless local Qdrant still works.
     */
    private static QdrantGrpcClient qdrantGrpc(Ctx ctx) {
        var builder = QdrantGrpcClient.newBuilder(ctx.qdrantHost, ctx.qdrantGrpcPort, false);
        String apiKey = System.getenv("QDRANT_API_KEY");
        if (apiKey != null && !apiKey.isBlank()) {
            builder = builder.withApiKey(apiKey);
        }
        return builder.build();
    }

    static VectorStore vectorStore(Ctx ctx, EmbeddingModel embed) {
        QdrantClient client = new QdrantClient(
                qdrantGrpc(ctx));
        return QdrantVectorStore.builder(client, embed)
                .collectionName(ctx.collection)
                .initializeSchema(false)
                .build();
    }
}
