package orbit;

import java.util.List;

import org.springframework.ai.chat.memory.ChatMemory;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.converter.BeanOutputConverter;

/**
 * Feature demo — exercises the Spring AI primitives Orbit actually relies on, on the real API,
 * offline, no key. Every line below runs the genuine framework:
 *   1. Working-memory hydration across turns (MessageChatMemoryAdvisor + MessageWindowChatMemory).
 *   2. Window eviction (bounded context) at the turn boundary.
 *   3. Structured output via the real BeanOutputConverter (format instructions, JSON schema, convert).
 *   4. Cost/observability probe (model-call counter; Usage metadata is source-verified in FEATURES).
 */
public final class FeatureDemoMain {

    public static void main(String[] args) {
        boolean ok = true;

        // 1. Working memory: three turns on one conversation; the model's echo shows the memory
        //    advisor injecting prior turns (message count grows across turns).
        Orbit orbit = new Orbit();
        String cid = Orbit.convId("acme", "alice", "billing", "s1");
        orbit.chat(cid, "my order id is 5567");
        orbit.chat(cid, "it never arrived");
        String r3 = orbit.chat(cid, "what's my order id?");
        boolean hydrated = r3.contains("5567");
        List<Message> stored = orbit.memory().get(cid);
        System.out.println("[1 working memory] turn 3 sees turn-1 order id via hydration: " + hydrated
                + " (stored msgs=" + stored.size() + ")");
        ok &= hydrated;

        // 2. Window eviction: maxMessages=4 keeps only the most-recent turns (bounded context).
        Orbit bounded = new Orbit(4);
        String bcid = Orbit.convId("acme", "bob", "billing", "s2");
        for (int i = 1; i <= 6; i++) {
            bounded.chat(bcid, "msg-" + i);
        }
        int kept = bounded.memory().get(bcid).size();
        boolean evicted = kept <= 4;
        System.out.println("[2 window eviction] after 6 turns, maxMessages=4 kept " + kept + " msgs (<=4): " + evicted);
        ok &= evicted;

        // 3. Structured output: the REAL BeanOutputConverter<Ticket> — format + schema + convert.
        BeanOutputConverter<Ticket> converter = new BeanOutputConverter<>(Ticket.class);
        String format = converter.getFormat();
        String schema = converter.getJsonSchema();
        boolean hasFormat = format != null && format.contains("JSON") && schema.contains("priority");
        // A provider would return this JSON; convert() parses it into the typed record.
        String modelJson = "{\"summary\":\"refund not received\",\"category\":\"billing\",\"priority\":2}";
        Ticket t = converter.convert(modelJson);
        boolean converted = t != null && "billing".equals(t.category()) && t.priority() == 2;
        System.out.println("[3 structured output] BeanOutputConverter format+schema present: " + hasFormat
                + "; convert() -> Ticket{" + t.category() + "," + t.priority() + "}: " + converted);
        ok &= hasFormat && converted;

        // 4. Cost/observability probe: how many times the model was actually invoked.
        System.out.println("[4 cost probe] model invocations this run (bounded bot): " + bounded.modelCalls()
                + " | Usage tokens live on ChatResponse.getMetadata() (source-verified, see FEATURES).");

        System.out.println();
        System.out.println(ok ? "RESULT: exit 0 — all feature checks passed on the real Spring AI API."
                : "RESULT: exit 1 — a feature check failed.");
        System.exit(ok ? 0 : 1);
    }
}
