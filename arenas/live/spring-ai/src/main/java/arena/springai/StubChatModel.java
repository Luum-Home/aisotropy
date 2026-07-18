package arena.springai;

import java.util.ArrayList;
import java.util.List;

import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.metadata.ChatResponseMetadata;
import org.springframework.ai.chat.metadata.DefaultUsage;
import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.chat.model.Generation;
import org.springframework.ai.chat.prompt.ChatOptions;
import org.springframework.ai.chat.prompt.Prompt;
import org.springframework.ai.model.tool.ToolCallingChatOptions;
import org.springframework.ai.tool.ToolCallback;

import reactor.core.publisher.Flux;

/**
 * Offline, deterministic {@link ChatModel} used when {@code ARENA_STUB=1}. Makes
 * ZERO network calls — it mirrors how every sibling adapter's stub lane swaps
 * the real model for an in-process fake (e.g. LangGraph's FakeListChatModel,
 * eino/genkit-go's local echo model). It:
 * <ul>
 *   <li>emits a deterministic echo reply for plain chat;</li>
 *   <li>emits a canned native tool-call when the prompt advertises tools, so
 *       {@code tools} mode can capture it single-shot;</li>
 *   <li>emits schema-shaped JSON when the prompt carries a structured-output
 *       format instruction, so {@code structured} mode's converter succeeds;</li>
 *   <li>attaches a deterministic fake token usage.</li>
 * </ul>
 */
final class StubChatModel implements ChatModel {

    private final String modelName;

    StubChatModel(String modelName) {
        this.modelName = modelName;
    }

    @Override
    public ChatResponse call(Prompt prompt) {
        AssistantMessage msg = buildMessage(prompt);
        return new ChatResponse(List.of(new Generation(msg)), metadata());
    }

    @Override
    public Flux<ChatResponse> stream(Prompt prompt) {
        String text = plainReply(prompt);
        String[] words = text.split(" ");
        List<ChatResponse> chunks = new ArrayList<>();
        StringBuilder acc = new StringBuilder();
        for (int i = 0; i < words.length; i++) {
            acc.append(i == 0 ? "" : " ").append(words[i]);
            boolean last = i == words.length - 1;
            AssistantMessage m = new AssistantMessage(words[i] + (last ? "" : " "));
            ChatResponseMetadata md = last ? metadata() : ChatResponseMetadata.builder().model(modelName).build();
            chunks.add(new ChatResponse(List.of(new Generation(m)), md));
        }
        if (chunks.isEmpty()) {
            chunks.add(new ChatResponse(List.of(new Generation(new AssistantMessage(""))), metadata()));
        }
        return Flux.fromIterable(chunks);
    }

    // ChatModel declares this; returning null is acceptable for a stub. Left
    // unannotated so the class still compiles if the interface method signature
    // differs across Spring AI patch releases.
    public ChatOptions getDefaultOptions() {
        return null;
    }

    // ---- reply construction ----

    private AssistantMessage buildMessage(Prompt prompt) {
        // 1) Tools advertised -> emit a canned native tool-call (captured, the
        //    real path never executes it either).
        List<ToolCallback> tools = advertisedTools(prompt);
        if (!tools.isEmpty()) {
            String name = tools.get(0).getToolDefinition().name();
            String args = "{\"expression\":\"47*89\"}";
            AssistantMessage.ToolCall tc = new AssistantMessage.ToolCall("stub-call-1", "function", name, args);
            return new AssistantMessage("", java.util.Map.of(), List.of(tc));
        }
        // 2) Structured-output request -> emit schema-shaped JSON.
        String promptText = promptText(prompt);
        if (looksStructured(promptText)) {
            String json = "{\"answer\":\"[spring-ai-stub] canned structured answer\","
                    + "\"confidence\":\"medium\",\"key_points\":[]}";
            return new AssistantMessage(json);
        }
        // 3) Plain chat.
        return new AssistantMessage(plainReply(prompt));
    }

    private String plainReply(Prompt prompt) {
        String user = lastUserText(prompt);
        if (user.isEmpty()) {
            return "[spring-ai-stub] OK";
        }
        return "[spring-ai-stub] " + user;
    }

    private ChatResponseMetadata metadata() {
        // Deterministic fake usage so token-plumbing runs end to end offline.
        return ChatResponseMetadata.builder()
                .model(modelName)
                .usage(new DefaultUsage(11, 7))
                .build();
    }

    private static List<ToolCallback> advertisedTools(Prompt prompt) {
        if (prompt.getOptions() instanceof ToolCallingChatOptions tco && tco.getToolCallbacks() != null) {
            return tco.getToolCallbacks();
        }
        return List.of();
    }

    private static boolean looksStructured(String text) {
        String t = text.toLowerCase();
        return t.contains("\"answer\"") && (t.contains("confidence") || t.contains("json"));
    }

    private static String promptText(Prompt prompt) {
        StringBuilder sb = new StringBuilder();
        prompt.getInstructions().forEach(m -> sb.append(m.getText()).append('\n'));
        return sb.toString();
    }

    private static String lastUserText(Prompt prompt) {
        var msgs = prompt.getInstructions();
        for (int i = msgs.size() - 1; i >= 0; i--) {
            String t = msgs.get(i).getText();
            if (t != null && !t.isEmpty()) {
                return t;
            }
        }
        return "";
    }
}
