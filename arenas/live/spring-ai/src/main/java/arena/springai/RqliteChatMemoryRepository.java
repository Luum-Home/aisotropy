package arena.springai;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import org.springframework.ai.chat.memory.ChatMemoryRepository;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.messages.UserMessage;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * A Spring AI {@link ChatMemoryRepository} backed by the SAME real networked
 * rqlite node the sibling adapters use (RQLITE_URL, default
 * {@code http://localhost:4001}), reached over rqlite's documented HTTP API
 * ({@code POST /db/execute}, {@code POST /db/query}).
 *
 * <p>This is the honest parity choice for {@code memory} mode: Spring AI ships
 * {@code JdbcChatMemoryRepository} for JDBC-speaking stores, but rqlite has no
 * bundled JDBC driver in this dependency graph — so this class implements the
 * SAME native SPI against rqlite's HTTP surface, exactly mirroring
 * {@code arenas/live/genkit-go/rqlite_client.go}. State written by one process
 * invocation is read back by a completely separate later invocation, proving
 * cross-process persistence.
 *
 * <p>When {@code ARENA_STUB=1}, an in-process map backs the store. HONEST
 * limitation (same as genkit-go's stub): a map cannot prove CROSS-process
 * persistence, so the stub lane only exercises within-process wiring.
 */
final class RqliteChatMemoryRepository implements ChatMemoryRepository {

    private static final String TABLE = "arena_spring_ai_memory";

    // Stub-lane in-process backing (within-process only).
    private static final Map<String, List<Message>> STUB = new HashMap<>();

    private final ObjectMapper mapper;
    private final String baseUrl;
    private final boolean stub;
    private final HttpClient http = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(10))
            .build();

    RqliteChatMemoryRepository(ObjectMapper mapper, String baseUrl, boolean stub) {
        this.mapper = mapper;
        this.baseUrl = baseUrl;
        this.stub = stub;
        ensureTable();
    }

    @Override
    public List<String> findConversationIds() {
        if (stub) {
            synchronized (STUB) {
                return new ArrayList<>(STUB.keySet());
            }
        }
        JsonNode res = query("SELECT DISTINCT conversation_id FROM " + TABLE);
        List<String> ids = new ArrayList<>();
        for (JsonNode row : values(res)) {
            ids.add(row.get(0).asText());
        }
        return ids;
    }

    @Override
    public List<Message> findByConversationId(String conversationId) {
        if (stub) {
            synchronized (STUB) {
                return new ArrayList<>(STUB.getOrDefault(conversationId, List.of()));
            }
        }
        JsonNode res = query(
                "SELECT role, content FROM " + TABLE + " WHERE conversation_id = ? ORDER BY seq ASC",
                conversationId);
        List<Message> out = new ArrayList<>();
        for (JsonNode row : values(res)) {
            out.add(toMessage(row.get(0).asText(), row.get(1).asText()));
        }
        return out;
    }

    @Override
    public void saveAll(String conversationId, List<Message> messages) {
        // SPI semantics: saveAll REPLACES the conversation's messages.
        if (stub) {
            synchronized (STUB) {
                STUB.put(conversationId, new ArrayList<>(messages));
            }
            return;
        }
        deleteByConversationId(conversationId);
        List<Object[]> stmts = new ArrayList<>();
        int seq = 0;
        for (Message m : messages) {
            stmts.add(new Object[] {
                    "INSERT INTO " + TABLE + "(conversation_id, seq, role, content) VALUES(?,?,?,?)",
                    conversationId, seq++, roleOf(m), m.getText()
            });
        }
        if (!stmts.isEmpty()) {
            execute(stmts);
        }
    }

    @Override
    public void deleteByConversationId(String conversationId) {
        if (stub) {
            synchronized (STUB) {
                STUB.remove(conversationId);
            }
            return;
        }
        execute(List.<Object[]>of(new Object[] {
                "DELETE FROM " + TABLE + " WHERE conversation_id = ?", conversationId }));
    }

    // ---- rqlite HTTP plumbing ----

    private void ensureTable() {
        if (stub) {
            return;
        }
        execute(List.<Object[]>of(new Object[] {
                "CREATE TABLE IF NOT EXISTS " + TABLE
                        + " (conversation_id TEXT, seq INTEGER, role TEXT, content TEXT)" }));
    }

    private JsonNode query(String sql, Object... args) {
        List<Object> stmt = new ArrayList<>();
        stmt.add(sql);
        for (Object a : args) {
            stmt.add(a);
        }
        return post("/db/query", List.of(stmt));
    }

    private void execute(List<Object[]> statements) {
        List<Object> body = new ArrayList<>();
        for (Object[] s : statements) {
            body.add(List.of(s));
        }
        post("/db/execute?transaction", body);
    }

    private JsonNode post(String path, Object body) {
        try {
            String json = mapper.writeValueAsString(body);
            HttpRequest req = HttpRequest.newBuilder()
                    .uri(URI.create(baseUrl + path))
                    .timeout(Duration.ofSeconds(15))
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(json))
                    .build();
            HttpResponse<String> resp = http.send(req, HttpResponse.BodyHandlers.ofString());
            if (resp.statusCode() / 100 != 2) {
                throw new RuntimeException("rqlite " + path + " returned HTTP " + resp.statusCode());
            }
            return mapper.readTree(resp.body());
        } catch (RuntimeException e) {
            throw e;
        } catch (Exception e) {
            throw new RuntimeException("rqlite " + path + ": " + e.getMessage(), e);
        }
    }

    /** Extracts the {@code results[0].values} array (empty if absent). */
    private static Iterable<JsonNode> values(JsonNode res) {
        JsonNode results = res.get("results");
        if (results == null || !results.isArray() || results.isEmpty()) {
            return List.of();
        }
        JsonNode vals = results.get(0).get("values");
        if (vals == null || !vals.isArray()) {
            return List.of();
        }
        return vals;
    }

    private static Message toMessage(String role, String content) {
        return switch (role) {
            case "assistant", "model" -> new AssistantMessage(content);
            case "system" -> new SystemMessage(content);
            default -> new UserMessage(content);
        };
    }

    private static String roleOf(Message m) {
        return m.getMessageType().getValue(); // "user" | "assistant" | "system"
    }
}
