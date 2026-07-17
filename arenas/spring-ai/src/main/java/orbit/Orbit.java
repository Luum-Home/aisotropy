package orbit;

import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.client.advisor.MessageChatMemoryAdvisor;
import org.springframework.ai.chat.memory.ChatMemory;
import org.springframework.ai.chat.memory.ChatMemoryRepository;
import org.springframework.ai.chat.memory.InMemoryChatMemoryRepository;
import org.springframework.ai.chat.memory.MessageWindowChatMemory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.chat.model.Generation;
import org.springframework.ai.chat.prompt.Prompt;

/**
 * Orbit — a minimal multi-tenant support chatbot built on the REAL Spring AI 2.0.0 API.
 *
 * <p>Wiring (all genuine Spring AI types, no provider key required):
 * <pre>
 *   ChatClient
 *     .defaultAdvisors( MessageChatMemoryAdvisor.builder(chatMemory).build() )
 *   chatMemory = MessageWindowChatMemory.builder()
 *                    .chatMemoryRepository( new InMemoryChatMemoryRepository() )
 *                    .maxMessages(20).build()
 *   chatModel  = EchoChatModel   // deterministic fake, implements the real ChatModel
 * </pre>
 *
 * <p><b>The isolation crux is visible right here.</b> Spring AI keys ALL conversation memory
 * by a single opaque {@code String conversationId} (see {@link ChatMemory#get(String)} and
 * {@link ChatMemoryRepository#findByConversationId(String)}). There is no tenant, user, product,
 * or authenticated-principal dimension anywhere in the memory API. To scope Orbit's memory by
 * four axes the developer must hand-concatenate them into that one string — {@link #convId} —
 * and the framework validates NOTHING about it.
 */
public final class Orbit {

    private final ChatClient chat;
    private final ChatMemory memory;
    private final ChatMemoryRepository repository;
    private final EchoChatModel model;

    public Orbit() {
        this(20);
    }

    public Orbit(int maxMessages) {
        this.repository = new InMemoryChatMemoryRepository();
        this.memory = MessageWindowChatMemory.builder()
                .chatMemoryRepository(this.repository)
                .maxMessages(maxMessages)
                .build();
        this.model = new EchoChatModel();
        this.chat = ChatClient.builder(this.model)
                .defaultAdvisors(MessageChatMemoryAdvisor.builder(this.memory).build())
                .build();
    }

    /**
     * Send a user turn on a conversation. The MessageChatMemoryAdvisor loads history for
     * {@code conversationId}, appends it to the prompt the model sees, then persists the new
     * user+assistant turn back under the same id — all keyed ONLY by that string.
     */
    public String chat(String conversationId, String userText) {
        return chat.prompt()
                .user(userText)
                .advisors(a -> a.param(ChatMemory.CONVERSATION_ID, conversationId))
                .call()
                .content();
    }

    /** Number of times the underlying model was actually invoked (cost/observability probe). */
    public int modelCalls() {
        return model.calls.get();
    }

    public ChatMemory memory() {
        return memory;
    }

    public ChatMemoryRepository repository() {
        return repository;
    }

    public ChatClient client() {
        return chat;
    }

    /**
     * The developer-authored composite key. Spring AI has no notion of these four axes — this
     * flat join is the ENTIRE isolation boundary, unescaped and unvalidated. Two distinct
     * identities whose segments collide across the delimiter map to the same key (see
     * {@code ProductIsolationMain} leak 3).
     */
    public static String convId(String tenant, String user, String product, String session) {
        return tenant + "|" + user + "|" + product + "|" + session;
    }

    /**
     * Deterministic keyless stand-in for a provider model. It implements the real
     * {@link ChatModel} interface, so ChatClient/advisors/memory all run through the genuine
     * Spring AI pipeline. The reply ECHOES every user message the model was handed — including
     * whatever conversation history the memory advisor injected — so a cross-tenant read of the
     * wrong conversationId surfaces the other tenant's secret verbatim in the response.
     */
    public static final class EchoChatModel implements ChatModel {
        final AtomicInteger calls = new AtomicInteger();

        @Override
        public ChatResponse call(Prompt prompt) {
            calls.incrementAndGet();
            StringBuilder recalled = new StringBuilder();
            List<Message> instructions = prompt.getInstructions();
            for (Message m : instructions) {
                // Echo only the user-turn texts the model can see (this is the injected memory).
                switch (m.getMessageType()) {
                    case USER -> {
                        if (recalled.length() > 0) recalled.append(" | ");
                        recalled.append(m.getText());
                    }
                    default -> { /* ignore system/assistant/tool */ }
                }
            }
            String reply = "Orbit-support recalled[" + instructions.size() + " msgs]: " + recalled;
            return new ChatResponse(List.of(new Generation(new AssistantMessage(reply))));
        }
    }
}
