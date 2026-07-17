package orbit;

import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.client.advisor.MessageChatMemoryAdvisor;
import org.springframework.ai.chat.memory.ChatMemory;
import org.springframework.ai.chat.memory.InMemoryChatMemoryRepository;
import org.springframework.ai.chat.memory.MessageWindowChatMemory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.chat.model.Generation;
import org.springframework.ai.chat.prompt.Prompt;
import org.springframework.ai.converter.BeanOutputConverter;
import org.springframework.ai.model.tool.StructuredOutputChatOptions;

/**
 * STEELMAN — what Spring AI 2.0.0 is genuinely great at, EXECUTED against the real framework,
 * offline, with no provider key. Isolation-by-default is the one narrow axis where aisotropy
 * differs; on the axis Spring AI is built for — the JVM's richest, fully self-hostable,
 * 100%-permissive agent SDK — Spring AI is strong and aisotropy does not try to compete.
 *
 * <p>Four strengths, each run through the genuine Spring AI pipeline (only the model is faked via
 * the real {@link ChatModel} seam; every ChatClient, advisor, memory, converter, and options object
 * is the shipped 2.0.0 code):
 * <ol>
 *   <li><b>S1 — ChatClient fluent API + advisor pipeline.</b> A one-liner fluent builder wires the
 *       memory advisor once; a multi-turn conversation hydrates automatically. RAN.</li>
 *   <li><b>S2 — Structured output the fluent way.</b> {@code call().entity(Ticket.class)} runs the
 *       real BeanOutputConverter end-to-end: schema-into-prompt, model JSON, typed record back. RAN.</li>
 *   <li><b>S3 — Bounded working memory.</b> {@link MessageWindowChatMemory} turn-aware eviction keeps
 *       the window at maxMessages and retains the SystemMessage. RAN.</li>
 *   <li><b>S4 — The NEW 2.0 self-correcting-output seam.</b> {@link StructuredOutputChatOptions}
 *       carries the JSON schema to the model layer for provider-native + schema-validation repair.
 *       The options object + schema are RAN here; the full repair loop needs a real provider
 *       (SOURCE — see FEATURES 2.2).</li>
 * </ol>
 * The {@code VectorStore} RAG abstraction is a genuine Spring AI strength too, but it ships in a
 * separate {@code spring-ai-vector-store} module not on this minimal chat classpath — covered
 * source-verified in FEATURES 1.4, not executed here.
 *
 * <p>Exit 0 = every executable strength ran as claimed on the real API.
 */
public final class StrengthsMain {

    public static void main(String[] args) {
        String line = "=".repeat(78);
        System.out.println(line);
        System.out.println("Spring AI 2.0.0 — STEELMAN: what it does genuinely well (real API, offline)");
        System.out.println(line);

        boolean ok = true;
        ok &= s1FluentClientAndMemory();
        ok &= s2FluentStructuredOutput();
        ok &= s3BoundedWindow();
        ok &= s4SelfCorrectingSeam();

        System.out.println();
        System.out.println("-".repeat(78));
        System.out.println("Honest framing: these are real Spring AI strengths. Spring AI is the JVM's");
        System.out.println("richest, fully self-hostable, 100%-permissive agent SDK — fluent ChatClient,");
        System.out.println("first-class ChatMemory/advisors, BeanOutputConverter, a VectorStore RAG");
        System.out.println("abstraction, and the new 2.0 self-correcting structured-output seam. aisotropy");
        System.out.println("differs on ONE narrow axis — isolation-by-default (all 4 scope axes bound from");
        System.out.println("context.Context as an unforgeable Scope) plus pre-spend governance and a Go");
        System.out.println("single-binary footprint. Spring AI wins the JVM ecosystem, maturity, and");
        System.out.println("self-hostable license posture. Different tools for different jobs.");
        System.out.println("-".repeat(78));

        if (!ok) {
            System.out.println("\nRESULT: exit 1 — a strength did not execute as claimed.");
            System.exit(1);
        }
        System.out.println("\nRESULT: exit 0 — all executable strengths ran against the real Spring AI API.");
        System.exit(0);
    }

    // S1 — Fluent ChatClient + the memory advisor pipeline. Register the advisor once on the
    // builder; every turn hydrates prior history automatically. This is the ergonomic core.
    private static boolean s1FluentClientAndMemory() {
        MessageWindowChatMemory memory = MessageWindowChatMemory.builder()
                .chatMemoryRepository(new InMemoryChatMemoryRepository())
                .maxMessages(20)
                .build();
        ChatClient chat = ChatClient.builder(new EchoModel())
                .defaultSystem("You are Orbit support.")
                .defaultAdvisors(MessageChatMemoryAdvisor.builder(memory).build())
                .build();

        String cid = "acme|alice|billing|s1";
        chat.prompt().user("my order id is 5567")
                .advisors(a -> a.param(ChatMemory.CONVERSATION_ID, cid)).call().content();
        String turn2 = chat.prompt().user("what was my order id?")
                .advisors(a -> a.param(ChatMemory.CONVERSATION_ID, cid)).call().content();

        boolean hydrated = turn2.contains("5567");
        System.out.println("[S1 fluent ChatClient + advisor] turn 2 auto-hydrated turn-1 order id: " + hydrated);
        return hydrated;
    }

    // S2 — Structured output via the fluent .entity() path. The real BeanOutputConverter injects the
    // schema into the prompt, the model returns JSON, and entity() parses it into a typed record.
    private static boolean s2FluentStructuredOutput() {
        ChatClient chat = ChatClient.builder(
                new JsonModel("{\"summary\":\"refund not received\",\"category\":\"billing\",\"priority\":2}"))
                .build();
        Ticket ticket = chat.prompt()
                .user("classify: customer says the refund never arrived")
                .call()
                .entity(Ticket.class);
        boolean ok = ticket != null && "billing".equals(ticket.category()) && ticket.priority() == 2;
        System.out.println("[S2 fluent structured output] call().entity(Ticket.class) -> Ticket{"
                + (ticket == null ? "null" : ticket.category() + "," + ticket.priority()) + "}: " + ok);
        return ok;
    }

    // S3 — Bounded working memory with turn-aware eviction. MessageWindowChatMemory keeps the window
    // at maxMessages and, unlike naive FIFO on raw messages, retains any SystemMessage in the store
    // across evictions. We seed a SystemMessage into memory, then overflow the window: the oldest
    // user/assistant turns are dropped but the SystemMessage survives.
    private static boolean s3BoundedWindow() {
        MessageWindowChatMemory memory = MessageWindowChatMemory.builder()
                .chatMemoryRepository(new InMemoryChatMemoryRepository())
                .maxMessages(4)
                .build();
        ChatClient chat = ChatClient.builder(new EchoModel())
                .defaultAdvisors(MessageChatMemoryAdvisor.builder(memory).build())
                .build();
        String cid = "acme|bob|billing|s2";
        // Seed a SystemMessage directly into the store (the window's retention rule applies to it).
        memory.add(cid, List.of(new SystemMessage("Orbit system prompt (must survive eviction).")));
        for (int i = 1; i <= 6; i++) {
            chat.prompt().user("msg-" + i)
                    .advisors(a -> a.param(ChatMemory.CONVERSATION_ID, cid)).call().content();
        }
        List<Message> kept = memory.get(cid);
        boolean systemSurvived = kept.stream().anyMatch(m -> m.getMessageType().name().equals("SYSTEM"));
        boolean bounded = kept.size() <= 4;
        System.out.println("[S3 bounded window] after 6 turns kept " + kept.size()
                + " msgs (<=4): " + bounded + "; seeded SystemMessage retained across eviction: " + systemSurvived);
        return bounded && systemSurvived;
    }

    // S4 — The NEW Spring AI 2.0 self-correcting-output seam. StructuredOutputChatOptions (2.0-only,
    // confirmed on the shipped classpath) carries the JSON schema down to the model layer so a
    // provider can do native + schema-validated repair. We build the real options object and read
    // the schema back; the full retry loop is provider-native (SOURCE, FEATURES 2.2).
    private static boolean s4SelfCorrectingSeam() {
        String schema = new BeanOutputConverter<>(Ticket.class).getJsonSchema();
        StructuredOutputChatOptions options = (StructuredOutputChatOptions) StructuredOutputChatOptions.builder()
                .outputSchema(schema)
                .build();
        String carried = options.getOutputSchema();
        boolean ok = carried != null && carried.contains("priority") && carried.equals(schema);
        System.out.println("[S4 2.0 self-correcting seam] StructuredOutputChatOptions carries the JSON schema"
                + " to the model layer: " + ok + " (full provider-native repair loop = SOURCE)");
        return ok;
    }

    // ---- offline fake models (real ChatModel impls) --------------------------------------------

    /** Echoes the user turns it can see — surfaces whatever memory the advisor injected. */
    static final class EchoModel implements ChatModel {
        final AtomicInteger calls = new AtomicInteger();
        @Override public ChatResponse call(Prompt prompt) {
            calls.incrementAndGet();
            StringBuilder recalled = new StringBuilder();
            for (Message m : prompt.getInstructions()) {
                if (m.getMessageType().name().equals("USER")) {
                    if (recalled.length() > 0) recalled.append(" | ");
                    recalled.append(m.getText());
                }
            }
            return new ChatResponse(List.of(new Generation(
                    new AssistantMessage("recalled: " + recalled))));
        }
    }

    /** Returns a fixed JSON payload so the fluent .entity() converter path runs end-to-end. */
    static final class JsonModel implements ChatModel {
        private final String json;
        JsonModel(String json) { this.json = json; }
        @Override public ChatResponse call(Prompt prompt) {
            return new ChatResponse(List.of(new Generation(new AssistantMessage(json))));
        }
    }
}
