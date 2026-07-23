package arena.springai;

import java.io.IOException;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * Spring AI (Java) live-arena adapter — multi-mode.
 *
 * <p>Reads exactly ONE JSON object from stdin, drives it through Spring AI's
 * native primitives against the real agnes-2.0-flash model (and, for
 * {@code rag}, the shared e5 embed server + Qdrant; for {@code memory}, real
 * networked rqlite), and writes exactly ONE JSON envelope to stdout. All
 * diagnostics go to stderr (prefix {@code PROGRESS:} by convention). This is
 * part of the {@code arenas/live/*} adapter family that all speak the same
 * stdin/stdout contract — see {@code arenas/live/SPEC.md}.
 *
 * <p>Modes (selected by the request's {@code "mode"} field, default
 * {@code "qa"}):
 * <ul>
 *   <li>{@code qa}         — single-shot chat (base SPEC.md contract).</li>
 *   <li>{@code rag}        — QdrantVectorStore retrieval + e5 embeddings + a
 *                            grounded agnes turn.</li>
 *   <li>{@code tools}      — native tool-calls CAPTURED, not executed
 *                            (internalToolExecutionEnabled=false).</li>
 *   <li>{@code memory}     — cross-process chat memory via the Spring AI
 *                            ChatMemoryRepository SPI over real rqlite.</li>
 *   <li>{@code structured} — BeanOutputConverter schema-constrained output.</li>
 *   <li>{@code stream}     — streaming ChatModel, ttft_ms measured.</li>
 * </ul>
 *
 * <p>Offline lane: {@code ARENA_STUB=1} swaps the real agnes chat model and the
 * real embed/vector/rqlite stores for in-process fakes that make ZERO network
 * calls (see {@link Models}, {@link StubChatModel}, {@link StubEmbeddingModel}).
 *
 * <p>Security: the API key ({@code OPENAI_API_KEY}) is never printed, logged, or
 * persisted; stdout carries only the JSON envelope.
 */
public final class Adapter {

    static final String FRAMEWORK = "spring-ai";

    private Adapter() {
    }

    public static void main(String[] args) {
        ObjectMapper mapper = new ObjectMapper();
        String model = envOr("OPENAI_MODEL", "agnes-2.0-flash");

        // The output is ALWAYS a single JSON object on stdout, even on failure
        // (contract rule 5: errors are data, not crashes).
        Map<String, Object> out = newEnvelope(model);

        String mode = "qa";
        try {
            Map<String, Object> in = readRequest(mapper);
            mode = strOr(in.get("mode"), "qa");
            out.put("mode", mode);

            Ctx ctx = Ctx.fromEnv(mapper, in, model, mode);
            log("PROGRESS: spring-ai adapter mode=" + mode + " stub=" + ctx.stub);

            switch (mode) {
                case "qa" -> Modes.qa(ctx, out);
                case "rag" -> Modes.rag(ctx, out);
                case "tools" -> Modes.tools(ctx, out);
                case "memory" -> Modes.memory(ctx, out);
                case "isolation_forgetful" -> Modes.isolationForgetful(ctx, out);
                case "structured" -> Modes.structured(ctx, out);
                case "stream" -> Modes.stream(ctx, out);
                default -> throw new IllegalArgumentException(
                        "unknown mode " + mode
                                + " (want one of: qa, rag, tools, memory, isolation_forgetful, structured, stream)");
            }
        } catch (Throwable t) {
            // Any failure becomes an error envelope with tokens -1.
            out.put("error", String.valueOf(t.getMessage() != null ? t.getMessage() : t));
            out.put("input_tokens", -1);
            out.put("output_tokens", -1);
            log("[spring-ai] error: " + t);
        }

        out.putIfAbsent("mode", mode);
        try {
            System.out.println(mapper.writeValueAsString(out));
        } catch (IOException e) {
            // Last-ditch minimal valid JSON.
            System.out.println("{\"framework\":\"" + FRAMEWORK + "\",\"mode\":\"" + mode
                    + "\",\"model\":\"" + model + "\",\"error\":\"failed to encode output\"}");
            System.exit(1);
        }
    }

    /** Builds the baseline envelope with contract-mandated defaults. */
    static Map<String, Object> newEnvelope(String model) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("framework", FRAMEWORK);
        out.put("mode", "qa");
        out.put("model", model);
        out.put("reply", "");
        out.put("input_tokens", -1);
        out.put("output_tokens", -1);
        out.put("latency_ms", 0L);
        out.put("tool_calls", new ArrayList<>());
        out.put("retrieved", new ArrayList<>());
        out.put("structured", null);
        out.put("ttft_ms", 0L);
        out.put("error", "");
        return out;
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> readRequest(ObjectMapper mapper) throws IOException {
        byte[] raw = System.in.readAllBytes();
        Object parsed = mapper.readValue(raw, Object.class);
        if (!(parsed instanceof Map)) {
            throw new IllegalArgumentException("stdin JSON must be an object");
        }
        return (Map<String, Object>) parsed;
    }

    // ---- small shared helpers ----

    static void log(String msg) {
        System.err.println(msg);
    }

    static String envOr(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isEmpty()) ? def : v;
    }

    static String strOr(Object v, String def) {
        return (v instanceof String s && !s.isEmpty()) ? s : def;
    }

    /** Returns the content of the final user-role message, or "". */
    @SuppressWarnings("unchecked")
    static String lastUserMessage(List<Map<String, Object>> messages) {
        if (messages == null) {
            return "";
        }
        for (int i = messages.size() - 1; i >= 0; i--) {
            Map<String, Object> m = messages.get(i);
            String role = strOr(m.get("role"), "user");
            if (role.equals("user")) {
                return strOr(m.get("content"), "");
            }
        }
        return "";
    }
}
