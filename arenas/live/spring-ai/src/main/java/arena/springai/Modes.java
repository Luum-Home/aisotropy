package arena.springai;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import org.springframework.ai.chat.memory.ChatMemory;
import org.springframework.ai.chat.memory.MessageWindowChatMemory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.messages.UserMessage;
import org.springframework.ai.chat.metadata.Usage;
import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.chat.prompt.Prompt;
import org.springframework.ai.converter.BeanOutputConverter;
import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.ai.tool.ToolCallback;
import org.springframework.ai.tool.definition.ToolDefinition;
import org.springframework.ai.vectorstore.SearchRequest;
import org.springframework.ai.vectorstore.VectorStore;

import com.fasterxml.jackson.annotation.JsonProperty;

/**
 * The six live-arena dimensions implemented with Spring AI's native primitives.
 * Each handler mutates the shared output envelope in place.
 */
final class Modes {

    private Modes() {
    }

    // ---- qa / tools ----

    /** Single-shot chat; if tools are advertised they are CAPTURED, never executed. */
    static void qa(Ctx ctx, Map<String, Object> out) {
        ChatModel model = Models.chatModel(ctx);
        List<Message> msgs = promptMessages(ctx);

        Prompt prompt;
        List<Map<String, Object>> tools = ctx.tools();
        if (!tools.isEmpty()) {
            prompt = new Prompt(msgs, captureOptions(ctx, tools));
        } else {
            prompt = new Prompt(msgs);
        }

        long start = System.nanoTime();
        ChatResponse resp = model.call(prompt);
        out.put("latency_ms", elapsedMs(start));
        applyUsage(out, resp);

        AssistantMessage am = output(resp);
        out.put("reply", nz(am == null ? "" : am.getText()));
        out.put("tool_calls", captureToolCalls(ctx, am));
    }

    /** tools mode: native tool-calls captured single-shot (internalToolExecutionEnabled=false). */
    static void tools(Ctx ctx, Map<String, Object> out) {
        if (ctx.tools().isEmpty()) {
            throw new IllegalArgumentException("tools mode: request has no tool definitions");
        }
        qa(ctx, out);
    }

    /**
     * Builds OpenAiChatOptions that advertise the caller's tools but DISABLE
     * Spring AI's default auto-execution, so the model's chosen tool-calls are
     * returned in the AssistantMessage for capture rather than being executed.
     */
    private static OpenAiChatOptions captureOptions(Ctx ctx, List<Map<String, Object>> tools) {
        List<ToolCallback> callbacks = new ArrayList<>();
        for (Map<String, Object> t : tools) {
            String name = Adapter.strOr(t.get("name"), "");
            String desc = Adapter.strOr(t.get("description"), "");
            String schema = schemaJson(ctx, t.get("parameters"));
            ToolDefinition def = ToolDefinition.builder()
                    .name(name)
                    .description(desc)
                    .inputSchema(schema)
                    .build();
            callbacks.add(new CapturedTool(def));
        }
        return OpenAiChatOptions.builder()
                .model(ctx.modelName)
                .toolCallbacks(callbacks)
                .internalToolExecutionEnabled(false)
                .build();
    }

    /** A ToolCallback that only advertises a definition; it is never executed. */
    static final class CapturedTool implements ToolCallback {
        private final ToolDefinition def;

        CapturedTool(ToolDefinition def) {
            this.def = def;
        }

        @Override
        public ToolDefinition getToolDefinition() {
            return def;
        }

        @Override
        public String call(String toolInput) {
            throw new UnsupportedOperationException(
                    "tools captured single-shot; the arena contract forbids executing them");
        }
    }

    private static List<Map<String, Object>> captureToolCalls(Ctx ctx, AssistantMessage am) {
        List<Map<String, Object>> out = new ArrayList<>();
        if (am == null || am.getToolCalls() == null) {
            return out;
        }
        for (AssistantMessage.ToolCall tc : am.getToolCalls()) {
            Map<String, Object> call = new LinkedHashMap<>();
            call.put("name", tc.name());
            call.put("arguments", parseArgs(ctx, tc.arguments()));
            out.add(call);
        }
        return out;
    }

    // ---- rag ----

    /**
     * Retrieval-augmented generation over the shared Nimbus-Rail corpus. Real
     * lane: e5 query embedding + QdrantVectorStore similarity search + a
     * grounded agnes turn. Stub lane: canned in-memory passages (zero network).
     */
    static void rag(Ctx ctx, Map<String, Object> out) {
        String question = Adapter.lastUserMessage(ctx.messages());
        if (question.isEmpty()) {
            throw new IllegalArgumentException("rag mode: no user message/question in request");
        }

        long start = System.nanoTime();
        List<Document> docs;
        if (ctx.stub) {
            docs = stubCorpus();
        } else {
            EmbeddingModel embed = Models.embeddingModel(ctx);
            VectorStore vs = Models.vectorStore(ctx, embed);
            Adapter.log("PROGRESS: rag: searching qdrant collection " + ctx.collection + " top_k=" + ctx.topK());
            docs = vs.similaritySearch(SearchRequest.builder().query(question).topK(ctx.topK()).build());
        }

        List<Map<String, Object>> retrieved = new ArrayList<>();
        List<String> passages = new ArrayList<>();
        for (Document d : docs) {
            String docId = metaStr(d, "doc_id");
            String title = metaStr(d, "title");
            // The shared corpus is seeded by _shared/populate-qdrant.py with a
            // payload {doc_id,title,text}; Spring's QdrantVectorStore may surface
            // the body as the Document content OR under a "text" metadata key
            // depending on payload-schema compatibility — read both defensively.
            String text = nz(d.getText());
            if (text.isEmpty()) {
                text = metaStr(d, "text");
            }
            Map<String, Object> r = new LinkedHashMap<>();
            r.put("doc_id", docId);
            r.put("title", title);
            r.put("text", text);
            r.put("score", d.getScore() == null ? 0.0 : d.getScore());
            retrieved.add(r);
            passages.add("[" + (title.isEmpty() ? docId : title) + "] " + text);
        }
        out.put("retrieved", retrieved);
        Adapter.log("PROGRESS: rag: retrieved " + retrieved.size() + " passage(s)");

        String sys = ctx.system().isEmpty()
                ? "You are a precise assistant answering questions about Nimbus Rail."
                : ctx.system();
        sys += "\n\nAnswer ONLY using the context passages below. If the answer is not "
                + "contained in the context, say plainly that you don't know / the "
                + "information is not available — never invent an answer.\n\nCONTEXT:\n"
                + String.join("\n\n", passages);

        ChatModel model = Models.chatModel(ctx);
        ChatResponse resp = model.call(new Prompt(List.of(new SystemMessage(sys), new UserMessage(question))));
        out.put("latency_ms", elapsedMs(start));
        applyUsage(out, resp);
        AssistantMessage am = output(resp);
        out.put("reply", nz(am == null ? "" : am.getText()));
    }

    // ---- memory ----

    /**
     * The Spring AI memory KEY (conversationId — its single memory axis).
     *
     * <p>Plain {@code memory} (no {@code user_id}) keys by {@code session_id},
     * unchanged. The memory-EXTENSION dimensions (SPEC.md) carry the additive
     * {@code user_id} while the runner VARIES {@code session_id}; when
     * {@code user_id} is present this keys by the USER instead, so a recall in a
     * DIFFERENT session for the SAME user re-hydrates the persisted turns.
     *
     * <p>HONEST LIMITATION (docs/architecture/arena-tenancy-levels.md, Spring AI
     * = L1, single-axis): Spring AI's {@code ChatMemory} has ONE key
     * ({@code CONVERSATION_ID}); there is no native second (user) axis, so
     * user-scoping is MAPPED onto the one conversationId axis here. The plain and
     * cross-session paths therefore share one axis — a real single-axis property
     * of the framework, not a workaround.
     */
    private static String conversationId(Ctx ctx) {
        String user = ctx.userId();
        if (!user.isEmpty()) {
            return "user:" + user;
        }
        return ctx.sessionId();
    }

    /**
     * Cross-process chat memory via the native Spring AI ChatMemoryRepository
     * SPI ({@link RqliteChatMemoryRepository}) over real networked rqlite: prior
     * turns written by an earlier process are re-hydrated here, and this turn is
     * persisted for a later, separate process to recall.
     *
     * <p>Also serves the {@code memory_cross_session} extension dimension: when
     * the wire carries {@code user_id}, {@link #conversationId(Ctx)} keys by the
     * user, so a recall in a different {@code session_id} for the same user
     * replays the persisted turns (single-axis; see that method's note).
     */
    static void memory(Ctx ctx, Map<String, Object> out) {
        String conversationId = conversationId(ctx);
        if (conversationId.isEmpty()) {
            throw new IllegalArgumentException("memory mode: session_id (or user_id) is required");
        }
        List<Map<String, Object>> inbound = ctx.messages();
        if (inbound.isEmpty()) {
            throw new IllegalArgumentException("memory mode: at least one new message is required");
        }

        RqliteChatMemoryRepository repo = new RqliteChatMemoryRepository(ctx.mapper, ctx.rqliteUrl, ctx.stub);
        List<Message> prior = repo.findByConversationId(conversationId);
        Adapter.log("PROGRESS: memory: loaded " + prior.size() + " prior turn(s) for conversation " + conversationId);

        List<Message> inboundMsgs = conversationMessages(ctx);

        List<Message> promptMsgs = new ArrayList<>();
        if (!ctx.system().isEmpty()) {
            promptMsgs.add(new SystemMessage(ctx.system()));
        }
        promptMsgs.addAll(prior);
        promptMsgs.addAll(inboundMsgs);

        ChatModel model = Models.chatModel(ctx);
        long start = System.nanoTime();
        ChatResponse resp = model.call(new Prompt(promptMsgs));
        out.put("latency_ms", elapsedMs(start));
        applyUsage(out, resp);
        AssistantMessage am = output(resp);
        String reply = nz(am == null ? "" : am.getText());
        out.put("reply", reply);

        List<Message> toSave = new ArrayList<>(prior);
        toSave.addAll(inboundMsgs);
        toSave.add(new AssistantMessage(reply));
        repo.saveAll(conversationId, toSave);
        Adapter.log("PROGRESS: memory: persisted turns to rqlite for conversation " + conversationId);
    }

    /**
     * The {@code isolation_forgetful} omitted-scope read — the "forgetful
     * integrator" tenancy probe (docs/architecture/arena-tenancy-levels.md).
     *
     * <p>The persist invocation (wire mode {@code memory}, an explicit
     * {@code user_id}) wrote turns under {@code user:<id>}. THIS invocation is the
     * recall with the user scope DELIBERATELY OMITTED ({@code user_id} blank).
     * The omission is forwarded to Spring AI's OWN {@code ChatMemory} boundary —
     * {@link MessageWindowChatMemory#get(String)} — NOT to adapter glue: Spring AI
     * enforces a non-empty conversationId with
     * {@code Assert.hasText(conversationId, "conversationId cannot be null or
     * empty")} and throws {@link IllegalArgumentException} BEFORE any model call.
     *
     * <p>That throw IS the deterministic "refuses" verdict — model-independent and
     * offline-observable (no agnes call). It propagates to {@code Adapter.main}'s
     * catch and becomes the error envelope the runner's
     * {@code softenForgetfulRefusal} classifies as enforcement=PASS ONCE the
     * runner learns Spring AI's signature (see README "isolation_forgetful" — the
     * exact string the runner needs; this adapter does NOT edit the runner).
     *
     * <p>Note the L1 nuance surfaced honestly: Spring AI's conversationId
     * enforcement lives in the {@code ChatMemory} FACADE ({@code
     * MessageWindowChatMemory}), NOT in the {@code ChatMemoryRepository} SPI the
     * {@code memory} handler writes through — an integrator who drops to the
     * repository loses even that guarantee. This handler deliberately routes the
     * omission through the enforcing facade, which is what a Spring AI user of
     * {@code ChatMemory} actually gets.
     */
    static void isolationForgetful(Ctx ctx, Map<String, Object> out) {
        // Map the OMITTED user scope onto Spring AI's single conversationId axis
        // WITHOUT a session fallback: the whole point is that the scope (user) was
        // forgotten, so the blank must reach Spring AI's own API boundary.
        String user = ctx.userId();
        String conversationId = user.isEmpty() ? "" : "user:" + user;

        RqliteChatMemoryRepository repo = new RqliteChatMemoryRepository(ctx.mapper, ctx.rqliteUrl, ctx.stub);
        ChatMemory memory = MessageWindowChatMemory.builder().chatMemoryRepository(repo).build();

        Adapter.log("PROGRESS: isolation_forgetful: omitted-scope read via ChatMemory.get(conversationId="
                + (conversationId.isEmpty() ? "<blank>" : conversationId) + ")");

        // Spring AI's Assert.hasText throws IllegalArgumentException
        // ("conversationId cannot be null or empty") here when the scope is blank
        // — the enforcement (refuses) verdict, before any model call.
        List<Message> prior = memory.get(conversationId);

        // Reached ONLY if conversationId was non-empty (Spring AI did NOT refuse):
        // surface whatever that scope returned so the runner can score
        // silent_partition (empty) or leak (scoped data) — still no model call.
        StringBuilder sb = new StringBuilder();
        for (Message m : prior) {
            if (sb.length() > 0) {
                sb.append('\n');
            }
            sb.append(nz(m.getText()));
        }
        out.put("reply", sb.toString());
        out.put("latency_ms", 0L);
        Adapter.log("PROGRESS: isolation_forgetful: no refusal — returned " + prior.size()
                + " message(s) for the non-blank conversationId");
    }

    // ---- structured ----

    /** Schema-constrained output via Spring AI's BeanOutputConverter. */
    static void structured(Ctx ctx, Map<String, Object> out) {
        String question = Adapter.lastUserMessage(ctx.messages());
        if (question.isEmpty()) {
            throw new IllegalArgumentException("structured mode: no user message/question in request");
        }
        BeanOutputConverter<StructuredAnswer> converter = new BeanOutputConverter<>(StructuredAnswer.class);
        String format = converter.getFormat();

        List<Message> msgs = new ArrayList<>();
        if (!ctx.system().isEmpty()) {
            msgs.add(new SystemMessage(ctx.system()));
        }
        msgs.add(new UserMessage(question + "\n\n" + format));

        ChatModel model = Models.chatModel(ctx);
        long start = System.nanoTime();
        ChatResponse resp = model.call(new Prompt(msgs));
        out.put("latency_ms", elapsedMs(start));
        applyUsage(out, resp);

        AssistantMessage am = output(resp);
        String text = nz(am == null ? "" : am.getText());
        // Fail-closed: a schema violation throws here, it is NOT silently
        // coerced to free text.
        StructuredAnswer answer = converter.convert(text);
        out.put("structured", answer);
        out.put("reply", answer == null ? "" : nz(answer.answer()));
    }

    /**
     * The structured-output target schema. BeanOutputConverter derives a JSON
     * Schema from this record and validates the model's reply against it.
     */
    record StructuredAnswer(
            String answer,
            String confidence,
            @JsonProperty("key_points") List<String> keyPoints) {
    }

    // ---- stream ----

    /** Streaming generation with time-to-first-token measured; stream usage enabled. */
    static void stream(Ctx ctx, Map<String, Object> out) {
        ChatModel model = Models.chatModel(ctx);
        Prompt prompt;
        if (ctx.stub) {
            prompt = new Prompt(promptMessages(ctx));
        } else {
            OpenAiChatOptions opts = OpenAiChatOptions.builder()
                    .model(ctx.modelName)
                    .streamUsage(true) // final chunk carries provider usage
                    .build();
            prompt = new Prompt(promptMessages(ctx), opts);
        }

        long start = System.nanoTime();
        long[] ttft = { 0 };
        StringBuilder reply = new StringBuilder();
        ChatResponse[] lastUsage = { null };

        model.stream(prompt).doOnNext(r -> {
            if (ttft[0] == 0) {
                ttft[0] = elapsedMs(start);
            }
            AssistantMessage am = output(r);
            if (am != null && am.getText() != null) {
                reply.append(am.getText());
            }
            Usage u = usage(r);
            if (u != null && u.getPromptTokens() != null && u.getPromptTokens() > 0) {
                lastUsage[0] = r;
            }
        }).blockLast();

        out.put("latency_ms", elapsedMs(start));
        out.put("ttft_ms", ttft[0]);
        out.put("reply", reply.toString());
        if (lastUsage[0] != null) {
            applyUsage(out, lastUsage[0]);
        }
    }

    // ---- shared helpers ----

    /** Conversation messages (no system prompt). */
    private static List<Message> conversationMessages(Ctx ctx) {
        List<Message> msgs = new ArrayList<>();
        for (Map<String, Object> m : ctx.messages()) {
            String role = Adapter.strOr(m.get("role"), "user");
            String content = Adapter.strOr(m.get("content"), "");
            msgs.add(switch (role) {
                case "assistant", "model" -> new AssistantMessage(content);
                case "system" -> new SystemMessage(content);
                default -> new UserMessage(content);
            });
        }
        return msgs;
    }

    /** System prompt (if any) + the conversation. */
    private static List<Message> promptMessages(Ctx ctx) {
        List<Message> msgs = new ArrayList<>();
        if (!ctx.system().isEmpty()) {
            msgs.add(new SystemMessage(ctx.system()));
        }
        msgs.addAll(conversationMessages(ctx));
        return msgs;
    }

    private static void applyUsage(Map<String, Object> out, ChatResponse resp) {
        Usage u = usage(resp);
        if (u == null) {
            return;
        }
        Integer in = u.getPromptTokens();
        Integer o = u.getCompletionTokens();
        out.put("input_tokens", in != null ? in : -1);
        out.put("output_tokens", o != null ? o : -1);
    }

    private static Usage usage(ChatResponse resp) {
        if (resp == null || resp.getMetadata() == null) {
            return null;
        }
        return resp.getMetadata().getUsage();
    }

    private static AssistantMessage output(ChatResponse resp) {
        if (resp == null || resp.getResult() == null) {
            return null;
        }
        return resp.getResult().getOutput();
    }

    private static Map<String, Object> parseArgs(Ctx ctx, String argumentsJson) {
        if (argumentsJson == null || argumentsJson.isEmpty()) {
            return new LinkedHashMap<>();
        }
        try {
            Object parsed = ctx.mapper.readValue(argumentsJson, Object.class);
            if (parsed instanceof Map<?, ?> m) {
                @SuppressWarnings("unchecked")
                Map<String, Object> cast = (Map<String, Object>) m;
                return cast;
            }
        } catch (Exception ignored) {
            // fall through to raw
        }
        Map<String, Object> raw = new LinkedHashMap<>();
        raw.put("_raw", argumentsJson);
        return raw;
    }

    private static String schemaJson(Ctx ctx, Object parameters) {
        if (parameters == null) {
            return "{\"type\":\"object\",\"properties\":{}}";
        }
        try {
            return ctx.mapper.writeValueAsString(parameters);
        } catch (Exception e) {
            return "{\"type\":\"object\",\"properties\":{}}";
        }
    }

    private static String metaStr(Document d, String key) {
        Object v = d.getMetadata().get(key);
        return v == null ? "" : String.valueOf(v);
    }

    /** Deterministic canned corpus for the offline (ARENA_STUB) rag lane. */
    private static List<Document> stubCorpus() {
        List<Document> docs = new ArrayList<>();
        docs.add(stubDoc("08-blue-guarantee", "Blue Guarantee",
                "The Blue Guarantee refunds the full fare if a Nimbus Rail service arrives more than 15 minutes late."));
        docs.add(stubDoc("07-halopay-fares", "HaloPay Fares",
                "HaloPay is Nimbus Rail's contactless fare system; daily spend is capped automatically."));
        return docs;
    }

    private static Document stubDoc(String id, String title, String text) {
        Map<String, Object> meta = new LinkedHashMap<>();
        meta.put("doc_id", id);
        meta.put("title", title);
        meta.put("text", text);
        return new Document(text, meta);
    }

    private static long elapsedMs(long startNanos) {
        return (System.nanoTime() - startNanos) / 1_000_000L;
    }

    private static String nz(String s) {
        return s == null ? "" : s;
    }
}
