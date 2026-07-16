package orbit;

import java.util.List;

/**
 * Adversarial isolation smoke — tenant/user axis, on the real Spring AI memory plane.
 *
 * <p>Pattern (identical to every arena): seed tenant A's secret in a conversation, run a
 * positive control (A reads its own), then run negative probes from other identities. A marker
 * appearing in a cross-tenant read is a proven leak.
 *
 * <p>Exit 0 = harness healthy AND the leak was demonstrated (positive control held, every
 * structural leak reproduced). Exit 1 = harness broken or a leak that should reproduce did not.
 */
public final class SmokeMain {

    static final String SECRET = "ORBIT-SECRET-acme-42-refund-approved";

    public static void main(String[] args) {
        Orbit orbit = new Orbit();
        boolean ok = true;

        // Tenant acme, user alice, product billing, a support session.
        String acmeKey = Orbit.convId("acme", "alice", "billing", "s1");
        orbit.chat(acmeKey, "Please note our refund policy secret: " + SECRET);

        // --- Positive control: acme/alice reads its own conversation -> MUST see the secret.
        String own = orbit.chat(acmeKey, "what did I just tell you?");
        boolean posOk = own.contains(SECRET);
        System.out.println("[positive control] acme/alice re-reads own convo -> secret present: " + posOk);
        ok &= posOk;

        // --- LEAK 1: shared/guessed conversationId. conversationId binds to NO principal, so any
        // caller that passes the same string gets the memory. A second tenant (globex) that a
        // developer accidentally maps to the same conversation key reads acme's secret.
        String globexReply = orbit.chat(acmeKey, "globex user hitting the same conversationId");
        boolean leak1 = globexReply.contains(SECRET);
        System.out.println("[LEAK 1: shared conversationId] globex caller on acme's key sees secret: " + leak1);
        ok &= leak1;

        // --- LEAK 2: org-wide enumeration. ChatMemoryRepository.findConversationIds() has NO
        // tenant filter — it returns EVERY tenant's conversation ids. Read any of them raw.
        List<String> allIds = orbit.repository().findConversationIds();
        boolean enumeratesCrossTenant = allIds.contains(acmeKey);
        System.out.println("[LEAK 2: org-wide scan] findConversationIds() returns all "
                + allIds.size() + " convos incl. acme's: " + enumeratesCrossTenant);
        boolean rawRead = orbit.repository().findByConversationId(acmeKey).stream()
                .anyMatch(m -> m.getText().contains(SECRET));
        System.out.println("[LEAK 2: raw read] findByConversationId(acmeKey) exposes secret: " + rawRead);
        ok &= enumeratesCrossTenant && rawRead;

        // --- Honesty control: a DIFFERENT, correctly-composed key has its own empty memory.
        // Exact-key scoping is SAFE *iff* the developer composes the right string every time.
        String globexKey = Orbit.convId("globex", "alice", "billing", "s1");
        String cleanReply = orbit.chat(globexKey, "hello from a correctly-scoped globex session");
        boolean honesty = !cleanReply.contains(SECRET);
        System.out.println("[honesty control] correctly-scoped globex key does NOT see acme secret: " + honesty);
        ok &= honesty;

        System.out.println();
        System.out.println("Isolation model: CONVENTION — memory keyed by a single opaque "
                + "conversationId String, bound to no authenticated principal.");
        System.out.println("Correct read and leaky read are one argument (or one map lookup) apart.");
        System.out.println(ok
                ? "RESULT: exit 0 — harness healthy, LEAK CONFIRMED (convention-based, real memory plane)."
                : "RESULT: exit 1 — harness broken or expected leak did not reproduce.");
        System.exit(ok ? 0 : 1);
    }
}
